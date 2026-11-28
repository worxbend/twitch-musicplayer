import { clamp } from '../viz/util.ts';
import type { AudioBands } from '../viz/types';
import { mediaStateOf, positionStateOf } from './mediaSession.ts';
import { followEnvelope, tauFor60fps } from './analysisMath.ts';

const BINS = 512;
// Visual envelopes as time constants in seconds, so 30 fps OBS sources and
// 144 Hz monitors move alike. Each matches the per-frame lerp factor it
// replaced at 60 fps. Spectrum: fast attack, slow release.
const SPECTRUM_ATTACK_SECS = tauFor60fps(0.45);
const SPECTRUM_RELEASE_SECS = tauFor60fps(0.12);
const BASS_SECS = tauFor60fps(0.4);
const MIDS_SECS = tauFor60fps(0.25);
const HIGHS_SECS = tauFor60fps(0.5);
const VOLUME_SECS = tauFor60fps(0.25);
// Slow running bass average that a kick must clear to count as a beat.
const BEAT_BASELINE_SECS = tauFor60fps(0.06);
const DEFAULT_FADE_OUT_MS = 2500;
const DEFAULT_FADE_IN_MS = 2500;
// A stream whose play() has not settled by then is treated as failed.
export const PLAY_START_TIMEOUT_MS = 20_000;
// A context resume that has not settled by then is treated as failed, so a play
// cannot hang on a context that waits for a gesture or an ended interruption.
export const RESUME_TIMEOUT_MS = 5_000;
// A started stream whose position has not moved for this long while playing is
// treated as failed: a half-open connection fires neither error nor ended.
export const STALL_TIMEOUT_MS = 30_000;
export const STALL_CHECK_MS = 5_000;
// Seconds before end to fire onNearEnd (triggers actual crossfade): fade + 0.5s network margin
const NEAR_END_THRESHOLD_SECS = DEFAULT_FADE_OUT_MS / 1000 + 0.5; // 3.0s
// Seconds before end to fire onPrefetch (triggers peek_next + audio preload)
const PREFETCH_THRESHOLD_SECS = 15;
const userGestureRequiredMessage = 'audio requires a user gesture';
const CROSSFADE_CURVE_STEPS = 64;
// Events that change what the OS progress bar should show. timeupdate is left out:
// the OS extrapolates the position from playbackRate.
const POSITION_EVENTS = ['loadedmetadata', 'durationchange', 'play', 'pause', 'ratechange', 'seeked', 'ended', 'emptied'];

// Builds an equal-power gain envelope from `from` to `to`. A rising envelope
// follows sin(s·π/2); a falling one follows cos(s·π/2). Paired rising/falling
// envelopes keep total power constant across a crossfade.
function equalPowerCurve(from: number, to: number): Float32Array {
  const steps = CROSSFADE_CURVE_STEPS;
  const curve = new Float32Array(steps);
  const rising = to >= from;
  const span = to - from;
  for (let i = 0; i < steps; i++) {
    const s = i / (steps - 1);
    const shape = rising ? Math.sin(s * Math.PI / 2) : Math.cos(s * Math.PI / 2);
    // rising:  from + span*sin  (0→1 shaping)
    // falling: to  + (-span)*cos (1→0 shaping, so it starts at `from`)
    curve[i] = rising ? from + span * shape : to - span * shape;
  }
  curve[steps - 1] = to;
  return curve;
}

interface AudioEngineOptions {
  allowAutoplay?: boolean;
}

type BrowserUserActivation = {
  isActive?: boolean;
  hasBeenActive?: boolean;
};

interface PlaybackChannel {
  audioEl: HTMLAudioElement;
  mediaSource: MediaElementAudioSourceNode;
  gain: GainNode;
  _nearEndCleanup: (() => void) | null;
  _stallCleanup: (() => void) | null;
}

export class AudioEngine {
  mode: 'idle' | 'backend' = 'idle';
  ctx: AudioContext | null = null;
  analyser: AnalyserNode | null = null;
  raw: Uint8Array = new Uint8Array(BINS);
  spectrum: Float32Array = new Float32Array(BINS);
  bands: AudioBands = { bass: 0, mids: 0, highs: 0, volume: 0 };
  beat = false;
  beatStrength = 0;
  hasUserSource = false;
  playing = false;
  sourceName = '';
  // Called when the active track's OS media state changes; feeds navigator.mediaSession.
  onPlayStateChange?: (state: MediaSessionPlaybackState) => void;
  // Called when the active track's progress changes; null clears it.
  onPositionChange?: (state: MediaPositionState | null) => void;

  private _playState: MediaSessionPlaybackState = 'none';
  private _bassAvg = 0;
  private _beatCooldown = 0;
  private _channelA: PlaybackChannel | null = null;
  private _channelB: PlaybackChannel | null = null;
  private _activeChannel: PlaybackChannel | null = null;
  // Master volume sits after the analyser, so visuals keep reacting when quiet or muted.
  private _master: GainNode | null = null;
  private _volume = 1;
  private _real: Uint8Array<ArrayBuffer> = new Uint8Array(BINS) as Uint8Array<ArrayBuffer>;
  private _t = 0;
  private _disposed = false;
  private _allowAutoplay: boolean;
  // Bumped by every playStream call; an older call that sees a newer value
  // leaves the channels alone, since the newer call now owns them.
  private _transition = 0;
  // The channel a crossfade is still fading out. A preload aimed at it waits in
  // _deferredPreload until the fade ends, so the fade tail and the preload both survive.
  private _fadingOut: PlaybackChannel | null = null;
  private _deferredPreload: string | null = null;
  // The transition token pause() last ran under. A play() that pause() aborts during
  // that transition's start window is a paused start, not a stream failure.
  private _pausedTransition: number | null = null;

  constructor(options: AudioEngineOptions = {}) {
    this._allowAutoplay = options.allowAutoplay === true;
  }

  get allowAutoplay() {
    return this._allowAutoplay;
  }

  private _ensureCtx() {
    if (this._disposed) return;
    if (this.ctx) return;
    this._assertCanStartAudio();

    const AC = window.AudioContext || (window as any).webkitAudioContext;
    this.ctx = new AC();
    this.analyser = this.ctx.createAnalyser();
    this.analyser.fftSize = 1024;
    this.analyser.smoothingTimeConstant = 0.82;
    this._real = new Uint8Array(this.analyser.frequencyBinCount) as Uint8Array<ArrayBuffer>;
    this._master = this.ctx.createGain();
    this._master.gain.value = this._volume;
    this.analyser.connect(this._master);
    this._master.connect(this.ctx.destination);
  }

  get volume() {
    return this._volume;
  }

  // Sets the listener's master volume (0..1). Channel gains stay crossfade envelopes.
  setVolume(volume: number): void {
    this._volume = Number.isNaN(volume) ? 1 : clamp(volume, 0, 1);
    if (!this.ctx || !this._master) return;
    const now = this.ctx.currentTime;
    this._master.gain.cancelScheduledValues(now);
    // A short time constant avoids a click when the level jumps.
    this._master.gain.setTargetAtTime(this._volume, now, 0.03);
  }

  async resume() {
    if (this._disposed) return;
    if (this._contextNeedsResume()) {
      this._assertCanStartAudio();
      await this._withTimeout(this.ctx!.resume(), RESUME_TIMEOUT_MS, () => this._resumeTimeoutError());
    }
  }

  // A click can rescue a hung resume only when autoplay is not allowed; an autoplay
  // page (OBS) gets a TimeoutError instead, so the caller retries on its own timer.
  private _resumeTimeoutError(): DOMException {
    const name = this._allowAutoplay ? 'TimeoutError' : 'NotAllowedError';
    return new DOMException('audio context did not resume', name);
  }

  // Safari/iOS reports 'interrupted' after a call, Siri or a lock; AudioContextState lacks it.
  private _contextNeedsResume(): boolean {
    const state = this.ctx?.state as string | undefined;
    return state === 'suspended' || state === 'interrupted';
  }

  private _assertCanStartAudio() {
    if (this.ctx && !this._contextNeedsResume()) return;
    if (this._allowAutoplay) return;
    if (this._hasUserActivation()) return;
    // Same name as the browser's own autoplay rejection, so callers can tell
    // "needs a click" apart from a broken stream with one check.
    throw new DOMException(userGestureRequiredMessage, 'NotAllowedError');
  }

  private _hasUserActivation() {
    const activation = (navigator as Navigator & { userActivation?: BrowserUserActivation }).userActivation;
    if (!activation) return true;
    return activation.isActive === true || activation.hasBeenActive === true;
  }

  // Builds one playback channel: an <audio> element wired through its own gain
  // node into the shared analyser. Two of these allow crossfading.
  private _createChannel(): PlaybackChannel {
    this._ensureCtx();
    if (!this.ctx || !this.analyser) throw new Error('audio engine disposed');

    const audioEl = new Audio();
    audioEl.autoplay = this._allowAutoplay;
    audioEl.crossOrigin = 'anonymous';
    audioEl.preload = 'auto';
    const mediaSource = this.ctx.createMediaElementSource(audioEl);
    const gain = this.ctx.createGain();
    gain.gain.value = 1;
    mediaSource.connect(gain);
    gain.connect(this.analyser);
    for (const type of ['play', 'pause', 'ended', 'emptied']) {
      audioEl.addEventListener(type, () => this._emitPlayState());
    }
    for (const type of POSITION_EVENTS) {
      audioEl.addEventListener(type, () => {
        if (audioEl === this._activeAudioElement()) this._emitPosition();
      });
    }

    return { audioEl, mediaSource, gain, _nearEndCleanup: null, _stallCleanup: null };
  }

  // Reads the active channel only, so the outgoing channel stopping after a crossfade
  // does not report 'paused'.
  private _emitPlayState() {
    const el = this._activeAudioElement();
    const state = mediaStateOf(el && { paused: el.paused, ended: el.ended, hasSource: !!el.src });
    if (state === this._playState) return;
    this._playState = state;
    this.onPlayStateChange?.(state);
  }

  private _emitPosition() {
    const el = this._activeAudioElement();
    this.onPositionChange?.(positionStateOf(el && {
      duration: el.duration,
      currentTime: el.currentTime,
      playbackRate: el.playbackRate,
      hasSource: !!el.src,
      ended: el.ended,
    }));
  }

  private _nextChannel(): PlaybackChannel {
    this._channelA ??= this._createChannel();
    this._channelB ??= this._createChannel();

    if (!this._activeChannel) return this._channelA;
    return this._activeChannel === this._channelA ? this._channelB : this._channelA;
  }

  private _activeAudioElement() {
    return this._activeChannel?.audioEl ?? null;
  }

  private _setChannelGain(channel: PlaybackChannel | null, value: number) {
    if (!channel) return;
    const clamped = clamp(value, 0, 1);
    if (!this.ctx) return;
    const now = this.ctx.currentTime;
    // A fade curve may still be running; setValueAtTime inside it throws NotSupportedError.
    channel.gain.gain.cancelScheduledValues(now);
    channel.gain.gain.setValueAtTime(clamped, now);
  }

  // Fades a channel's gain using an equal-power curve. A rising fade follows
  // sin(t·π/2) and a falling fade follows cos(t·π/2); when the two channels of a
  // crossfade run together their powers sum to a constant, so there is no dip in
  // perceived loudness in the middle of the transition (a linear crossfade would
  // sag by ~6 dB at the midpoint).
  private _fadeChannelTo(channel: PlaybackChannel | null, value: number, durationMs: number): Promise<void> {
    if (!channel || !this.ctx) return Promise.resolve();

    const clamped = clamp(value, 0, 1);
    const now = this.ctx.currentTime;
    const durationSec = Math.max(0, durationMs) / 1000;
    const gain = channel.gain.gain;
    const from = clamp(gain.value, 0, 1);

    gain.cancelScheduledValues(now);
    if (durationSec === 0 || Math.abs(clamped - from) < 1e-4) {
      gain.setValueAtTime(clamped, now);
      return Promise.resolve();
    }

    const curve = equalPowerCurve(from, clamped);
    try {
      gain.setValueCurveAtTime(curve, now, durationSec);
    } catch {
      // setValueCurveAtTime can throw if events overlap; fall back to a ramp.
      gain.setValueAtTime(from, now);
      gain.linearRampToValueAtTime(clamped, now + durationSec);
    }
    return new Promise((resolve) => {
      window.setTimeout(resolve, Math.max(0, Math.ceil(durationMs)));
    });
  }

  // Releases a channel's media element and audio graph. Used only by dispose;
  // _stopChannel is the milder version that keeps the nodes connected for reuse.
  private _teardownChannel(channel: PlaybackChannel | null) {
    if (!channel) return;
    this._releaseElement(channel);
    channel.mediaSource.disconnect();
    channel.gain.disconnect();
  }

  // Detaches handlers and the current source from a channel's <audio> element.
  private _releaseElement(channel: PlaybackChannel) {
    this._stopWatching(channel);
    channel.audioEl.pause();
    channel.audioEl.onended = null;
    channel.audioEl.onerror = null;
    channel.audioEl.removeAttribute('src');
    channel.audioEl.load();
  }

  private _stopChannel(channel: PlaybackChannel | null) {
    if (!channel) return;
    this._releaseElement(channel);
    this._setChannelGain(channel, 0);
  }

  // Loads the next song's URL on the inactive channel for buffering without playing.
  preloadNext(url: string): void {
    if (!this.ctx) return;
    try {
      const ch = this._nextChannel();
      if (ch === this._fadingOut) {
        this._deferredPreload = url;
        return;
      }
      this._loadIntoChannel(ch, url);
    } catch { /* ignore */ }
  }

  private _loadIntoChannel(ch: PlaybackChannel, url: string) {
    if (ch.audioEl.src === url && ch.audioEl.error === null) return;
    this._stopWatching(ch);
    ch.audioEl.pause();
    ch.audioEl.onended = null;
    ch.audioEl.onerror = null;
    ch.audioEl.src = url;
    ch.audioEl.load();
  }

  // Drops the fade-out claim on a channel, with any preload waiting for it.
  private _releaseFadeClaim(channel: PlaybackChannel | null) {
    if (!channel || channel !== this._fadingOut) return;
    this._fadingOut = null;
    this._deferredPreload = null;
  }

  async playStream(
    streamUrl: string,
    sourceName: string,
    handlers: {
      onEnded?: () => void;
      onError?: (error: Event) => void;
      onPrefetch?: () => void;   // fired PREFETCH_THRESHOLD_SECS before end — request next song URL
      onNearEnd?: () => void;    // fired NEAR_END_THRESHOLD_SECS before end — start crossfade
      onStarted?: () => void;    // fired once the new track is audible, before the crossfade completes
      onStall?: () => void;      // fired once if the started track stops progressing for STALL_TIMEOUT_MS
    } = {},
  ): Promise<void> {
    await this.resume();
    const outgoing = this._activeChannel;
    const incoming = this._nextChannel();
    const token = ++this._transition;
    const superseded = () => token !== this._transition;
    const wasPlaying = !!(outgoing && this.playing && !outgoing.audioEl.paused);
    // Checked even when paused: an older, superseded call may still resolve play() on it.
    const outgoingHasSource = !!outgoing?.audioEl.src;
    const hadSource = this.mode === 'backend' && outgoingHasSource;
    // A new play may claim the channel an older crossfade is still fading out.
    this._releaseFadeClaim(incoming);

    // Clean up any stale watchers on the incoming channel.
    this._stopWatching(incoming);

    this._setChannelGain(incoming, 0);
    incoming.audioEl.onended = null;
    incoming.audioEl.onerror = null;
    incoming.audioEl.pause();
    incoming.audioEl.loop = false;
    incoming.audioEl.onended = handlers.onEnded ?? null;
    incoming.audioEl.onerror = handlers.onError ? event => handlers.onError?.(event instanceof Event ? event : new Event('error')) : null;

    // Skip reload if this channel already has the correct URL buffered (e.g. after preloadNext),
    // unless the preload errored.
    if (incoming.audioEl.src !== streamUrl || incoming.audioEl.error !== null) {
      incoming.audioEl.src = streamUrl;
      incoming.audioEl.load();
    }

    this.mode = 'backend';
    this.hasUserSource = true;
    this.sourceName = sourceName;
    this._activeChannel = incoming;
    try {
      const started = incoming.audioEl.play();
      // play() clears paused at once, so a track change reports 'playing' with no 'paused' blip.
      this._emitPlayState();
      this._emitPosition();
      await this._withTimeout(
        started,
        PLAY_START_TIMEOUT_MS,
        () => new DOMException('stream did not start', 'TimeoutError'),
      );
      if (superseded()) return;
      this.playing = true;
      handlers.onStarted?.();

      this._watchNearEnd(incoming, handlers);
      this._watchStall(incoming, handlers);

      if (outgoingHasSource) this._fadingOut = outgoing;
      const fadeIn = this._fadeChannelTo(incoming, 1, DEFAULT_FADE_IN_MS);
      const fadeOut = outgoingHasSource ? this._fadeChannelTo(outgoing, 0, DEFAULT_FADE_OUT_MS) : Promise.resolve();
      await Promise.all([fadeIn, fadeOut]);
      if (superseded()) {
        this._releaseFadeClaim(outgoing);
        return;
      }

      if (outgoingHasSource) {
        this._stopChannel(outgoing);
        this._finishFadeOut(outgoing);
      }
    } catch (error) {
      this._releaseFadeClaim(outgoing);
      if (!superseded() && this._isPausedStart(error, token)) {
        // The user paused before the stream started: keep the new track loaded and paused.
        this._stopChannel(outgoing);
        this._setChannelGain(incoming, 1);
        this.playing = !incoming.audioEl.paused;
        handlers.onStarted?.();
        this._watchNearEnd(incoming, handlers);
        this._watchStall(incoming, handlers);
        this._emitPlayState();
        this._emitPosition();
        return;
      }
      if (superseded()) {
        if (!this._disposed && incoming !== this._activeChannel) this._stopChannel(incoming);
        throw error;
      }
      this._stopChannel(incoming);
      this._activeChannel = outgoing;
      if (outgoing && wasPlaying) {
        this._setChannelGain(outgoing, 1);
        this.playing = true;
      } else {
        this.playing = false;
      }
      this.mode = hadSource ? 'backend' : 'idle';
      this._emitPlayState();
      this._emitPosition();
      throw error;
    }
  }

  private _isPausedStart(error: unknown, token: number): boolean {
    return error instanceof DOMException && error.name === 'AbortError' && this._pausedTransition === token;
  }

  // Wires up near-end monitoring (prefetch + crossfade triggers) on a started channel.
  private _watchNearEnd(
    incoming: PlaybackChannel,
    handlers: { onPrefetch?: () => void; onNearEnd?: () => void },
  ) {
    if (handlers.onPrefetch || handlers.onNearEnd) {
      const el = incoming.audioEl;
      let prefetchFired = false;
      let nearEndFired = false;
      const timeHandler = () => {
        if (nearEndFired) return;
        const { duration, currentTime } = el;
        if (!duration || !isFinite(duration) || currentTime < 2.0) return;
        const remaining = duration - currentTime;
        if (!prefetchFired && remaining < PREFETCH_THRESHOLD_SECS) {
          prefetchFired = true;
          handlers.onPrefetch?.();
        }
        if (remaining < NEAR_END_THRESHOLD_SECS) {
          nearEndFired = true;
          el.removeEventListener('timeupdate', timeHandler);
          incoming._nearEndCleanup = null;
          handlers.onNearEnd?.();
        }
      };
      el.addEventListener('timeupdate', timeHandler);
      incoming._nearEndCleanup = () => el.removeEventListener('timeupdate', timeHandler);
    }
  }

  // Removes a channel's near-end and stall watchers.
  private _stopWatching(channel: PlaybackChannel) {
    channel._nearEndCleanup?.();
    channel._nearEndCleanup = null;
    channel._stallCleanup?.();
    channel._stallCleanup = null;
  }

  // Fires onStall once when the active channel plays but its position stops moving.
  // Paused time never counts, and a channel fading out is left to the stop path.
  private _watchStall(channel: PlaybackChannel, handlers: { onStall?: () => void }) {
    const { onStall } = handlers;
    if (!onStall) return;
    const el = channel.audioEl;
    let lastTime = el.currentTime;
    let lastProgressAt = Date.now();
    const onTimeUpdate = () => {
      if (el.currentTime === lastTime) return;
      lastTime = el.currentTime;
      lastProgressAt = Date.now();
    };
    const onPlay = () => { lastProgressAt = Date.now(); };
    const stop = () => {
      window.clearInterval(timer);
      el.removeEventListener('timeupdate', onTimeUpdate);
      el.removeEventListener('play', onPlay);
      channel._stallCleanup = null;
    };
    const timer = window.setInterval(() => {
      const stalled = !el.paused && !el.ended && !!el.src && Date.now() - lastProgressAt >= STALL_TIMEOUT_MS;
      if (!stalled || channel !== this._activeChannel) return;
      stop();
      onStall();
    }, STALL_CHECK_MS);
    el.addEventListener('timeupdate', onTimeUpdate);
    el.addEventListener('play', onPlay);
    channel._stallCleanup = stop;
  }

  // Rejects with makeError() when `pending` has not settled within `ms`, e.g. a stalled
  // proxy or a context that never resumes. Promise.race also handles a late rejection
  // of `pending`, so it is never unhandled.
  private async _withTimeout(pending: Promise<void>, ms: number, makeError: () => DOMException): Promise<void> {
    let timer: number | undefined;
    const timeout = new Promise<never>((_, reject) => {
      timer = window.setTimeout(() => reject(makeError()), ms);
    });
    try {
      await Promise.race([pending, timeout]);
    } finally {
      window.clearTimeout(timer);
    }
  }

  // Ends the fade-out claim on a stopped channel and runs the preload that waited for it.
  private _finishFadeOut(channel: PlaybackChannel | null) {
    if (!channel || channel !== this._fadingOut) return;
    const url = this._deferredPreload;
    this._fadingOut = null;
    this._deferredPreload = null;
    if (url) this._loadIntoChannel(channel, url);
  }

  // Resumes the active track. Does nothing when no track is loaded, it is already playing,
  // or it has ended (an ended element is paused too, and play() would restart it from 0).
  play(): boolean {
    const active = this._activeAudioElement();
    if (this.mode === 'backend' && active?.paused && !active.ended && active.src) {
      // The element routes through the context, so a suspended context plays silence.
      // Called from a user gesture: resume directly, skipping the autoplay assertion.
      if (this._contextNeedsResume()) void this.ctx!.resume().catch(() => {});
      void active.play().catch(() => { this.playing = false; });
      this.playing = true;
    }
    return this.playing;
  }

  // Pauses the active track. Does nothing when no track is loaded or it is already paused.
  pause(): boolean {
    const active = this._activeAudioElement();
    if (this.mode === 'backend' && active && !active.paused) {
      active.pause();
      this._fadingOut?.audioEl.pause();
      this._pausedTransition = this._transition;
      this.playing = false;
    }
    return this.playing;
  }

  togglePlay(): boolean {
    return this._activeAudioElement()?.paused ? this.play() : this.pause();
  }

  // Returns the active track's playback position, or null when nothing is loaded.
  // duration is 0 while the stream's metadata has not yet loaded.
  getPlaybackProgress(): { currentTime: number; duration: number } | null {
    const el = this._activeAudioElement();
    if (!el || this.mode !== 'backend') return null;
    const duration = Number.isFinite(el.duration) ? el.duration : 0;
    return { currentTime: el.currentTime || 0, duration };
  }

  private _synth(dt: number, intensity: number) {
    this._t += dt;
    const t = this._t;
    const BPM = 76, beat = 60 / BPM, bar = beat * 4;
    const inBar = t % bar;
    const macro = 0.62 + 0.38 * (0.5 + 0.5 * Math.sin(t * 0.26));

    const kicks = [0, beat * 1.5, beat * 2, beat * 3.5];
    let kick = 0;
    for (const k of kicks) {
      let d = inBar - k; if (d < 0) d += bar;
      kick = Math.max(kick, Math.exp(-d / 0.14));
    }
    const sub = 0.5 + 0.5 * Math.sin(t * 2.0);

    let hat = 0;
    for (let i = 0; i < 8; i++) {
      const ht = i * (beat / 2) + beat / 4;
      let d = inBar - ht; if (d < 0) d += bar;
      hat = Math.max(hat, Math.exp(-d / 0.035));
    }

    const noise = (x: number) =>
      0.5 + 0.5 * (Math.sin(x * 12.9 + t * 1.7) * 0.5 + Math.sin(x * 41.3 - t * 0.9) * 0.3 + Math.sin(x * 91.1 + t * 3.1) * 0.2);

    for (let i = 0; i < BINS; i++) {
      const f = i / BINS;
      let v = 0;
      if (i < 44) {
        const shape = Math.pow(1 - i / 44, 1.4);
        v = (kick * 0.95 + sub * 0.32) * shape;
      } else if (i < 170) {
        const md = Math.pow(1 - (i - 44) / 126, 0.9);
        const mel = 0.22 + 0.18 * Math.sin(t * 1.3 + f * 9) + 0.14 * Math.sin(t * 0.5 + f * 22);
        v = (mel + 0.12 * noise(f)) * md * 0.9;
      } else {
        const hd = Math.pow(1 - (i - 170) / (BINS - 170), 1.3);
        v = (hat * 0.55 + 0.06 + 0.16 * noise(f * 3)) * hd * 0.7;
      }
      v = Math.max(0, v) * macro * intensity;
      this.raw[i] = clamp(v * 255, 0, 255);
    }
  }

  /** True while the active element plays backend audio; `playing` stays set after an end or an external pause. */
  get audible(): boolean {
    const el = this._activeAudioElement();
    return (
      this.mode === 'backend' &&
      !!this.analyser &&
      this.playing &&
      !!el && !el.paused && !el.ended
    );
  }

  update(dt: number, intensity = 1) {
    if (this._disposed) return;

    if (this.audible) {
      this.analyser!.getByteFrequencyData(this._real);
      const n = Math.min(BINS, this._real.length);
      for (let i = 0; i < n; i++) this.raw[i] = this._real[i];
    } else {
      this._synth(dt, intensity);
    }

    for (let i = 0; i < BINS; i++) {
      this.spectrum[i] = followEnvelope(
        this.spectrum[i], this.raw[i] / 255, SPECTRUM_ATTACK_SECS, SPECTRUM_RELEASE_SECS, dt,
      );
    }

    const avg = (a: number, b: number) => {
      let s = 0; for (let i = a; i < b; i++) s += this.raw[i];
      return s / ((b - a) * 255);
    };
    const follow = (cur: number, target: number, tau: number) =>
      followEnvelope(cur, target, tau, tau, dt);
    const instBass = avg(1, 12);
    this.bands.bass = follow(this.bands.bass, instBass, BASS_SECS);
    this.bands.mids = follow(this.bands.mids, avg(34, 120), MIDS_SECS);
    this.bands.highs = follow(this.bands.highs, avg(150, 360), HIGHS_SECS);
    this.bands.volume = follow(this.bands.volume, avg(1, 380), VOLUME_SECS);

    this._bassAvg = follow(this._bassAvg, instBass, BEAT_BASELINE_SECS);
    this._beatCooldown -= dt;
    this.beat = false;
    if (instBass > this._bassAvg * 1.32 && instBass > 0.18 && this._beatCooldown <= 0) {
      this.beat = true;
      this.beatStrength = clamp((instBass - this._bassAvg) * 3, 0.3, 1.2);
      this._beatCooldown = 0.16;
    }
  }

  dispose() {
    if (this._disposed) return;
    this._disposed = true;
    // Supersedes any in-flight playStream so a late play() settle cannot touch released channels.
    this._transition++;
    this.playing = false;
    this.mode = 'idle';
    this.hasUserSource = false;
    this.sourceName = '';

    this._teardownChannel(this._channelA);
    this._teardownChannel(this._channelB);
    this._channelA = null;
    this._channelB = null;
    this._activeChannel = null;
    this._fadingOut = null;
    this._deferredPreload = null;
    this.analyser?.disconnect();
    this.analyser = null;
    this._master?.disconnect();
    this._master = null;
    const ctx = this.ctx;
    this.ctx = null;
    if (ctx && ctx.state !== 'closed') {
      ctx.close().catch(() => {
        // Already closed or torn down by the browser; teardown is best-effort.
      });
    }
    this.onPositionChange?.(null);
  }
}
