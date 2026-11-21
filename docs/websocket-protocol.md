<div align="center">

<a href="../README.md"><img src="../frontend/src/assets/worxbend-logo.png" width="72" alt="Lofi Radio" /></a>

# 🔌 WebSocket Protocol

### *The message contract between the backend DJ and its players.*

[🏠 Home](../README.md) · [🗺️ Overview](overview.md) · [🎧 User](user-guide.md) · [🛠️ Dev](developer-guide.md) · [🌐 API](api.md) · **🔌 WebSocket** · [⚙️ Config](configuration.md) · [🚀 Deploy](deployment.md)

</div>

---

Endpoint:

```text
ws://localhost:8080/ws
```

## ✉️ Message Envelope

All messages use:

```json
{
  "type": "message_type",
  "payload": {}
}
```

The server reads at most 4096 bytes per client frame. A larger frame closes
the connection with code 1009 (message too big).

## 📤 Client To Server

### `need_song`

Requests the next playable song.

```json
{"type":"need_song"}
```

### `song_finished`

Reports that the current streamed song ended.

```json
{
  "type": "song_finished",
  "payload": {
    "song_id": "song-id",
    "history_id": "history-id"
  }
}
```

### `heartbeat`

Application-level heartbeat.

```json
{"type":"heartbeat"}
```

## 📥 Server To Client

### `play_song`

Instructs the frontend to stream a song.

```json
{
  "type": "play_song",
  "payload": {
    "song": {
      "id": "song-id",
      "filename": "track.mp3",
      "title": "track",
      "artist": "",
      "album": "",
      "duration_secs": 0,
      "size_bytes": 12345,
      "added_at": "2026-06-06T12:00:00Z"
    },
    "stream_url": "http://localhost:8080/v1/songs/song-id/content",
    "history_id": "history-id",
    "queue_depth": 0
  }
}
```

### `now_playing`

Broadcast to every client when a song starts, after its `play_song` was delivered.

```json
{
  "type": "now_playing",
  "payload": {
    "song": { "id": "song-id", "title": "track", "artist": "" },
    "queue_depth": 0,
    "history_id": "history-id"
  }
}
```

`history_id` matches the `play_song.history_id` of that start. It is absent when the server
re-announces the current song after its tags change, so the `/now-playing` overlay can
re-show a repeated title on a new start (`?hold=N`) without re-showing it on a retag.

### `queue_updated`

Broadcast when queue state changes.

```json
{
  "type": "queue_updated",
  "payload": {
    "queue_depth": 1,
    "reason": "song_added"
  }
}
```

Known reasons:

- `song_added`
- `song_removed`
- `song_moved` (an admin moved a queued item up or down)
- `skipped`
- `cleared`
- `new_file`
- `file_removed` (a library file or directory was deleted or renamed away)
- `file_updated` (a library file was rewritten or replaced and its tags changed)

### `error`

```json
{
  "type": "error",
  "payload": {
    "message": "queue is empty"
  }
}
```

### `heartbeat_ack`

```json
{"type":"heartbeat_ack"}
```

### `state`

Sent after connection with a lightweight state snapshot. It is also broadcast to every
client when a `song_finished` meets an empty `manual_only` queue, or an empty library under
an auto strategy; `current_song` is then absent, so overlays and the admin console stop
showing the finished song. Because the player sends `song_finished` at the crossfade lead,
this happens up to ~3s before the audio ends. A `need_song` on an empty queue never clears it: a second player tab sends one on
connect while the first may still be playing.

```json
{
  "type": "state",
  "payload": {
    "current_song": { "id": "...", "title": "...", "artist": "..." },
    "queue_length": 0,
    "total_songs": 0
  }
}
```

`current_song` is omitted while nothing is playing. The counts are looked up on a context
detached from the HTTP upgrade request, so they stay accurate after the handler returns.

## 🤝 Frontend Contract

The frontend:

1. Connects to `/ws`.
2. Sends `need_song`.
3. Plays `play_song.payload.stream_url`.
4. Sends `song_finished` on media `ended`.
5. Waits for the next `play_song`.

The frontend does not fetch MP3 bytes into JavaScript. It assigns `stream_url` to a native media element.

## 🛡️ Resilience Contract

The supported frontend is a single active WebSocket playback client. It is designed to stay connected indefinitely:

- reconnects with exponential backoff up to a capped delay;
- listens for browser `online` and `offline` events;
- sends application heartbeats;
- reconnects when heartbeat acknowledgements or other messages stop arriving;
- queues important outbound control messages while disconnected;
- flushes queued messages after reconnect;
- requests a song again after reconnect when no song is active, or when a `need_song`/`song_finished` went out on a socket that closed before the reply;
- re-sends `need_song` if no `play_song` arrives within 15 seconds;
- retries after an `error` reply with exponential backoff from 900 ms up to 30 seconds, reset by the next successful play;
- retries a pending stream if browser autoplay blocked playback;
- retries stream playback after media element errors, or a stream that has not started within 20 seconds, every 5 seconds, and after 3 failed retries of the same song sends `need_song` to skip it.

The server protocol remains intentionally simple because only one active playback client is supported.
