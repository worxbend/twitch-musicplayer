<div align="center">

<a href="../README.md"><img src="../frontend/src/assets/worxbend-logo.png" width="72" alt="Lofi Radio" /></a>

# 📋 Requirement Review

### *Original requirements checked against what shipped.*

[🏠 Home](../README.md) · [🗺️ Overview](overview.md) · [🎧 User](user-guide.md) · [🛠️ Dev](developer-guide.md) · [🌐 API](api.md) · [🔌 WebSocket](websocket-protocol.md) · [⚙️ Config](configuration.md) · [🚀 Deploy](deployment.md)

</div>

---

This document reviews the implementation against the original requested behavior.

## Implemented

### Go backend service

Implemented in `backend/` with Chi, Gorilla WebSocket, an in-memory index with a Bloom-filter history, fsnotify, and ID3 metadata extraction.

### go-blueprint style setup

The backend follows the generated-style layout with `cmd/api`, `internal/*`, `Makefile`, `.env`, and config files.

### Host files from a specified folder

Implemented through `MUSIC_DIR` and `[music].dir`. Startup scan and watcher index `.mp3` files.

### HTTP resource-oriented API

Implemented under `/v1`:

- Songs.
- Queue.
- Player state and skip. `GET /v1/player` includes the current song, held by
  `player.StateTracker` (`backend/internal/player/state.go`).
- History. `GET /v1/history` returns recent song starts, newest first, from a bounded
  in-memory ring of `player.MaxHistory` (50) entries in `player.StateTracker`. It resets on
  restart. The Bloom filter is separate and only steers automatic selection.

Streaming endpoints use resource/action style:

- `/v1/songs/{id}/content`
- `/v1/songs/{id}:stream`

### WebSocket endpoint

Implemented at `/ws`.

### Single active WebSocket client model

Implemented as the supported deployment model. The frontend assumes it is the only playback client and drives queue/history progression.

### Client/server playback flow

Implemented:

```text
need_song -> play_song -> song_finished -> play_song
```

### Efficient frontend streaming

Implemented. The frontend assigns `stream_url` to `HTMLAudioElement.src`. It does not fetch the MP3 into JavaScript.

### History-backed automatic selection

Implemented. Default shuffle strategy is `round_robin`, which avoids automatic repeats until the known library cycles.

### Manual duplicate queue entries

Implemented. `POST /v1/queue` can manually add the same song multiple times.

### New file monitoring

Implemented in `backend/internal/watcher/watcher.go`. New files, including files in nested
folders, are indexed once their size stops changing and then added to the queue.

### File deletion and rename reconciliation

Implemented in `backend/internal/watcher/watcher.go`. Remove and rename events drop the
song from the library and the queue; a rename's new path is indexed as a new file. An
fsnotify overflow triggers a full resync with the disk.

### Backend-only frontend mode

Implemented. Local file and microphone modes are removed from the supported frontend code path. Visible load/mic/play/tweaks buttons are removed. Visual styles are selected by route rather than by an in-page panel.

### Resilient WebSocket frontend

Implemented. The frontend reconnects continuously with backoff, sends heartbeats, detects stale sockets, queues outbound control messages while disconnected, flushes them after reconnect, and retries pending playback/stream failures.

### Canvas/WebGL optimization

Implemented as a first pass:

- WebGL preference.
- 60 fps ticker cap.
- Lower resolution cap.
- Lower default effect density.
- Lower bloom quality.
- Lower geometry counts.
- Reused per-frame geometry buffers.

## Partially Implemented

### Health endpoint

Partially implemented. `/health` exists, but the response is minimal and should be improved for production.

## Not Implemented Yet

- Authentication/authorization for queue mutation.
- Production-grade WebSocket origin restrictions (`CheckOrigin` accepts every origin).
- An end-to-end WebSocket protocol test over a real socket (see the
  [Contributor Guide](contributor-guide.md#-testing-gaps-to-close)).
