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

Audio players connect with `?role=player` (`ws://localhost:8080/ws?role=player`), so the
backend counts them as players from the start. Admin consoles and overlays omit it. Other
values are ignored.

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

A browser upgrade whose `Origin` is not in `server.allowed_origins` (or the
`server.base_url` origin) is refused with HTTP 403. Clients that send no
`Origin` header connect normally.

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
    "reason": "song_added",
    "next_song": { "id": "...", "title": "...", "artist": "..." }
  }
}
```

Known reasons:

- `song_added`
- `song_removed`
- `song_moved` (an admin moved a queued item up or down)
- `skipped`
- `play_next` (an admin queued a song to play immediately; follows `skip_now`)
- `cleared`
- `new_file`
- `file_removed` (a library file or directory was deleted or renamed away)
- `file_updated` (a library file was rewritten or replaced and its tags changed)
- `song_started` (the server sent the next song to the player and took it off the queue)
- `song_blocked` (an admin pulled a song out of rotation; its queued entries are gone)
- `song_unblocked` (an admin put a blocked song back in rotation)

`queue_depth` is the live queue length after the change, for every reason.

`next_song` is the first playable queued song, the one the next `need_song` would get from
the queue. Queued entries whose file is gone are skipped. The key is absent when nothing
playable is queued. An auto strategy may still pick a song then, but that pick is not known
in advance. The `/now-playing?next=1` overlay shows it as its "Up next" line.

### `error`

```json
{
  "type": "error",
  "payload": {
    "code": "queue_empty",
    "message": "queue is empty"
  }
}
```

Sent to the player when `need_song` or `song_finished` cannot get a song. `code` is a closed enum:

| `code` | Meaning |
| --- | --- |
| `queue_empty` | The `manual_only` queue is empty. |
| `library_empty` | The library has no playable songs. |
| `internal` | Any other server error. The details go to the server log, never to the client. |

`message` is display-only and may change. Clients must branch on `code`, and must treat an
unknown or missing `code` (for example from an older server) as `internal`.

### `heartbeat_ack`

```json
{"type":"heartbeat_ack"}
```

### `state`

Sent as the first frame after connection, with a lightweight state snapshot. It is also broadcast to every
client when a `song_finished` meets an empty `manual_only` queue, or an empty library under
an auto strategy; `current_song` is then absent, so overlays and the admin console stop
showing the finished song. The player sends `song_finished` at the crossfade lead, about 3s
before the audio ends, so the cleared `state` goes out about 4s after the `song_finished`,
once the tail has played out. A song that starts inside that window cancels the clear and
stays current. A `need_song` on an empty queue never clears it: a second player tab sends
one on connect while the first may still be playing.

The same cleared `state` is broadcast about 5s after the last player connection closes. A
connection is a player once it connects with `?role=player` or sends `need_song`,
`song_finished` or `peek_next`. A player reconnecting inside that window, or a legacy client
that reconnects without the parameter and sends `need_song`, keeps `current_song`. A reconnect
after the window leaves `current_song` absent until the next `play_song`. A silent network drop
is noticed only after the pong timeout (60s). A graceful server shutdown clears `current_song`
and empties the OBS now-playing file; a crash or `SIGKILL` leaves the file until the next start,
which empties it.

```json
{
  "type": "state",
  "payload": {
    "current_song": { "id": "...", "title": "...", "artist": "..." },
    "queue_length": 1,
    "total_songs": 0,
    "next_song": { "id": "...", "title": "...", "artist": "..." }
  }
}
```

`total_songs` counts indexed songs in rotation; blocked songs are not counted. It matches
`GET /v1/player`.

`current_song` is omitted while nothing is playing. `next_song` follows the `queue_updated`
rule and is also in `GET /v1/player`. The counts are looked up on a context
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
- retries a socket whose handshake has not finished within 10 seconds, on the same backoff;
- queues important outbound control messages while disconnected;
- flushes queued messages after reconnect;
- requests a song again after reconnect when no song is active, or when a `need_song`/`song_finished` went out on a socket that closed before the reply;
- re-sends `need_song` if no `play_song` arrives within 15 seconds;
- retries after an `error` reply with exponential backoff from 900 ms up to 30 seconds, reset by the next successful play;
- holds back an `error` that arrives while a handed-off track's tail is still audible, and reports it when the tail ends, stalls or errors, or at the latest about 2 seconds after its expected end (a tail resumed after a pause can push this back by up to 30 seconds);
- retries a pending stream if browser autoplay blocked playback;
- retries stream playback after media element errors, a stream that has not started within 20 seconds, or a started stream whose position has not moved for 30 seconds while playing, every 5 seconds, and after 3 failed retries of the same song sends `need_song` to skip it. A pause before the stream starts is not a failure: the song stays loaded and paused.

The server protocol remains intentionally simple because only one active playback client is supported.
