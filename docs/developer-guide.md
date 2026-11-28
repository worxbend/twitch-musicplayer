<div align="center">

<a href="../README.md"><img src="../frontend/src/assets/worxbend-logo.png" width="72" alt="Lofi Radio" /></a>

# 🛠️ Developer Guide

### *Code map, playback flow, and how the pieces fit together.*

[🏠 Home](../README.md) · [🗺️ Overview](overview.md) · [🎧 User](user-guide.md) · **🛠️ Dev** · [🌐 API](api.md) · [🔌 WebSocket](websocket-protocol.md) · [⚙️ Config](configuration.md) · [🚀 Deploy](deployment.md)

</div>

---

## 🐳 Running In Containers

If you only want the app running, not a development loop:

```bash
docker compose up -d --build
```

See the [Deployment Guide](deployment.md) for the three compose files and every setting.
The rest of this page covers running the services directly, which is what you want when
editing code — the containers do not hot-reload.

## 📦 Prerequisites

- Go 1.27 (`backend/go.mod` pins `toolchain go1.27.4`).
- Node.js 26+ (`engines` in `frontend/package.json`; `frontend/.nvmrc` selects 26).
- npm.
- The Docker build images, every CI `setup-go` and `package.json` (`engines.node`, `@types/node`) follow `backend/go.mod`'s toolchain (Go major.minor) and `frontend/.nvmrc` (Node major). So do the Go and Node versions named in `README.md`, this guide and `backend/README.md`, and the README TypeScript badge (vs `frontend/package.json`); `scripts/check-toolchains.sh` enforces this in CI and `make check`.
- No CGO toolchain required: the service uses no database driver.
- MP3 files for local playback.

## 🎛️ Backend Setup

```bash
cd backend
cp .env.example .env
cp config.example.toml config.toml
mkdir -p music
go mod download
go run ./cmd/api
```

The backend starts at `http://localhost:8080`.

## 🖼️ Frontend Setup

```bash
cd frontend
cp .env.example .env.local
npm install
npm run dev
```

The frontend starts at `http://localhost:3000`.

## 🗺️ Backend Code Map

- `cmd/api/main.go`: startup, dependency wiring, graceful shutdown.
- `internal/config`: TOML and environment loading.
- `internal/repository`: in-memory song and queue stores.
- `internal/played`: Bloom-filter play-history tracker, persisted to a single file.
- `internal/queue`: song selection, queue operations, history start/finish.
- `internal/watcher`: startup scan, fsnotify create and remove handling, and metadata refresh when an indexed file is rewritten or replaced.
- `internal/api`: HTTP handlers under `/v1`.
- `internal/websocket`: protocol messages, hub, client session loop.
- `internal/meta`: ID3 metadata extraction.

## 🧭 Frontend Code Map

**Entry points** — `src/index.tsx` picks a root component from `window.location.pathname`.

- `src/App.tsx`: main scene, wires the playback client to the UI.
- `src/OverlayApp.tsx`: chrome-free scene for OBS browser sources.
- `src/SceneApp.tsx`: shared player and transparent source for all catalogue scenes.
- `src/ScenesIndex.tsx`: instrument collection, ramp selection, and OBS URLs.
- `src/AdminApp.tsx` / `src/admin.tsx`: admin console (separate `admin.html` bundle).

**Audio**

- `src/audio/BackendPlaybackClient.ts`: WebSocket controller — reconnect, heartbeat,
  outbox, prebuffering, and the typed `RadioStatus` reported to the UI.
- `src/audio/AudioEngine.ts`: two-channel playback, equal-power crossfade, FFT analyser.
- `src/audio/autoplay.ts`: decides whether audio may start without a user gesture.

**Rendering**

- `src/components/Stage.tsx`: Pixi application lifecycle and the ticker; takes a
  visualizer factory so each page can mount a different scene.
- `src/components/PlayerUI.tsx`: now-playing card, progress bar, clock, live badge.
- `src/viz/scenes.ts`: the scene catalogue. Adding a row routes the scene and
  lists it on `/scenes`; update the documentation and SVG study separately.
- `src/viz/WaveScene.ts`: shared base for the waveform and spectrum scenes.
  Owns the Pixi lifecycle, the backdrop and the bloom layer; subclasses only
  implement `draw()`.
- `src/viz/palette.ts`: the colour ramps, selectable per page with `?ramp=`.
- `src/viz/LofiRainVisualizer.ts`: the signature orbital record and fine-rain composition.
- [Signal Observatory](signal-observatory/README.md): current designs, safe canvas placement,
  lifecycle contracts, screenshots, and browser verification.
- `src/viz/OscilloscopeVisualizer.ts`, `BarSpectrumVisualizer.ts`,
  `MirrorWaveVisualizer.ts`, `DotMatrixVisualizer.ts`, `RibbonVisualizer.ts`,
  `LineWaveVisualizer.ts`, `LensWaveVisualizer.ts`: the individual scenes.
- `src/viz/types.ts`: the `Visualizer` interface and the `AudioFrame` contract.

> [!IMPORTANT]
> `WaveScene` deliberately does not call its own `onResize()` hook from its
> constructor. A subclass's fields are not initialised until after `super()`
> returns, so an override touching subclass state would read `undefined`. The
> first `update()` primes it instead.

### 🎚️ Audio analysis

`src/audio/AudioAnalysisEngine.ts` turns raw analyser output into series that
are actually drawable, and every scene reads from it rather than touching an
`AnalyserNode`. It fixes three things about raw FFT data:

- **Log-spaced bands.** FFT bins are linear in frequency, so half a linear
  spectrum covers 11–22 kHz where there is nothing to see. Bands are spaced by
  octave instead, giving bass and treble equal screen width.
- **Tilt.** Musical energy falls off steeply with frequency, so an untreated
  spectrum looks like a cliff. About +4.5 dB per octave flattens it.
- **Envelope reduction.** Time-domain data is reduced to per-bucket minimum and
  maximum, not sampled every Nth point. Point sampling aliases badly: a loud
  steady tone becomes a thin flickering line, because each sample lands wherever
  the oscillation happens to be rather than at its extreme.

The two reductions live in `src/audio/analysisMath.ts` as pure functions so they
can be tested without a browser or an audio device.

## 🔄 Backend Playback Flow

1. Frontend opens `/ws`.
2. Frontend sends `need_song`.
3. Backend dequeues the next song (marking it played), sends `play_song` with a fresh `history_id`, records the start in the in-memory history, and broadcasts `now_playing`.
4. Frontend loads the `stream_url` (`/v1/songs/{id}/content`) on the idle audio channel and crossfades it in with an equal-power curve.
5. About 15 seconds before the end, frontend sends `peek_next`; backend answers `prebuffer_song` and the next track buffers on the idle channel without being dequeued.
6. About 3 seconds before the end (or on media `ended`), frontend sends `song_finished`.
7. Backend answers with the next `play_song`, or clears now-playing when the queue or library is empty.

## 📥 Queue Fill Behavior

`auto_refill` keeps `min_ahead` automatic songs queued after each selection. `preload` keeps `preload_size` songs queued. Manual queue additions are serialized with playback advancement, and queue position assignment is serialized in the in-memory repository so entries have deterministic ordering.

## 🛡️ WebSocket Resilience

`BackendPlaybackClient` is expected to run continuously:

- reconnects with capped exponential backoff;
- monitors browser network events;
- sends heartbeat messages;
- treats a quiet socket as stale and reconnects;
- queues outbound control messages while disconnected;
- re-requests music when connected and idle, when a song request's reply is lost with its socket, or after 15 seconds without a reply;
- backs off `error` retries exponentially up to 30 seconds;
- retries pending playback after a user gesture if autoplay blocks;
- retries stream playback after media errors, a 20-second start timeout, or a 30-second mid-track stall, then skips the song with `need_song` after 3 failed retries.

## 🔒 Backend-Only Frontend Mode

The frontend intentionally supports only backend streams:

- No local file picker.
- No microphone input.
- No visible play/pause button.
- The top-right chrome displays the current backend-provided song name under the clock.

This keeps playback state aligned with the backend protocol and avoids competing local audio modes.

## 🪟 Overlay Routes

`src/index.tsx` selects a root component by pathname:

| Pathname | Component | Transparent |
|:--|:--|:--|
| `/` | `App` | no |
| `/overlay` | `OverlayApp` | no |
| `/logo`, `/logo-overlay` | `OverlayApp` | yes |
| `/scenes` | `ScenesIndex` | no |
| `/now-playing` | `NowPlayingOverlay` | yes |
| `/<slug>` (every scene in `src/viz/scenes.ts`) | `SceneApp` | no |
| `/<slug>-overlay` | `SceneApp` | yes |

`?transparent=1` forces transparency on the opaque `/<slug>` scene routes (`matchScene` in
`src/viz/scenes.ts`); it does not affect `/`, `/overlay` or `/now-playing`.

`OverlayApp` adds the `overlay-page` class to the document element, and its transparent
variant also adds `logo-overlay-page`, which drops the page background so OBS can composite
the canvas over other sources. Transparent `SceneApp` routes and `/now-playing` add
`logo-overlay-page` only; opaque scenes add the scene's own page class (`scene-page` by
default). Each class is removed on cleanup, so switching routes in a single-page session
does not leave styling behind.

## ✅ Verification

Run every CI gate from the repo root with one command:

```bash
make check              # toolchain parity, lint, backend check, frontend
                        # npm ci + audit + typecheck + test + build
make ui                 # opt-in: Playwright (needs chromium installed)
make docker-smoke       # opt-in: builds both images, runs scripts/docker-smoke.sh
                        # and scripts/compose-smoke.sh (hub/local compose
                        # hardening parity, then docker-compose.yml hardening)
                        # on ports 18081/13001 (override with SMOKE_BE_PORT=...
                        # SMOKE_FE_PORT=...), then removes the images
```

`ui` and `docker-smoke` stay out of `make check` because both are heavy on a shared host.
The blocks below are the per-gate detail.

```bash
make -C backend check   # gofmt, go mod tidy -diff, go vet, staticcheck,
                        # go test -race, govulncheck, go build
```

```bash
cd frontend
npm ci
npm audit --omit=dev --audit-level=high   # production deps: no high/critical advisories
npm run typecheck       # typecheck src, tests and configs
npm test                # unit tests, via node --test
npm run build           # typecheck + production bundle
npx playwright install chromium
npm run test:ui         # Chromium artwork, accessibility, playback and interaction checks
```

```bash
bash scripts/lint.sh    # actionlint over workflows, shellcheck over scripts/ and
                        # frontend/docker-entrypoint.d (needs go + shellcheck 0.11.0)
```

```bash
bash scripts/docker-smoke.sh BACKEND_IMAGE FRONTEND_IMAGE
                        # hardened-container smoke of built images; ports via
                        # SMOKE_BE_PORT / SMOKE_FE_PORT (default 8080 / 3000), needs node
bash scripts/compose-smoke.sh BACKEND_IMAGE FRONTEND_IMAGE
                        # fails when docker-compose.hub.yml hardening drifts from
                        # docker-compose.yml, then starts docker-compose.yml with
                        # the images under its own project; ports via SMOKE_BE_PORT /
                        # SMOKE_FE_PORT (default 18082 / 13002), needs jq
```

CI (`.github/workflows/ci.yml`) runs the same gates in six jobs: `backend`, `frontend`,
`ui` (Playwright), `lint` (`scripts/lint.sh`), `docker` (compose validation, image builds,
`scripts/docker-smoke.sh` and `scripts/compose-smoke.sh`) and `docker-arm64` (native arm64
image builds on an arm64 runner, an architecture check and the same smoke script).

The product site has its own workflow (`.github/workflows/pages.yml`). `make site` runs the
same steps locally through `scripts/site.sh`: it assembles `site/` with the logo and
screenshots into a temp dir (needs `cwebp` from the `webp` package), then fails on a missing
asset, a site over the 2.5 MB budget, a `site/tuner.js` syntax error, a tuner route with no
frontend page (`frontend/src/routes.ts` or a scene slug in `frontend/src/viz/scenes.ts`), or
tuner radios in `site/index.html` that differ from the scenes in `tuner.js`. It stays out of
`make check`.

Frontend unit tests run in plain Node, which executes the TypeScript directly.
Browser tests use Playwright and a separate Vite server on port 3100.

> [!NOTE]
> Run the backend tests with `-race`. Both of the concurrency bugs the hub tests cover were
> the kind that only show up under the race detector or under load. 🏁

The frontend build can warn about chunk size because Pixi and filters are large dependencies. That warning does not mean the build failed.

## ⚡ Performance Work

The visualizer targets WebGL rendering at 60 fps:

- Pixi is configured with WebGL preference and high-performance GPU mode.
- Device pixel ratio is capped to reduce fill-rate cost.
- Ticker is capped at 60 fps.
- Default bloom, chromatic split, and particle density are conservative.
- Several per-frame geometry arrays are reused instead of allocated.

Use browser dev tools performance profiling before increasing defaults.
