<div align="center">

<a href="../README.md"><img src="../frontend/src/assets/worxbend-logo.png" width="72" alt="Lofi Radio" /></a>

# 🌐 Backend API

### *Every HTTP endpoint under `/v1`, with request and response shapes.*

[🏠 Home](../README.md) · [🗺️ Overview](overview.md) · [🎧 User](user-guide.md) · [🛠️ Dev](developer-guide.md) · **🌐 API** · [🔌 WebSocket](websocket-protocol.md) · [⚙️ Config](configuration.md) · [🚀 Deploy](deployment.md)

</div>

---

Base URL:

```text
http://localhost:8080
```

## 🩺 Health

```http
GET /health
```

Returns `200` when the HTTP process is alive.

## 🎵 Songs

```http
GET /v1/songs
```

Response:

```json
{
  "songs": [],
  "total": 0
}
```

```http
GET /v1/songs/{id}
```

Returns a song resource.

```http
GET /v1/songs/{id}/content
GET /v1/songs/{id}:stream
```

Streams MP3 content. The backend uses `http.ServeFile`, so range requests are supported by the standard library.

## 📋 Queue

```http
GET /v1/queue
```

Returns pending queue items.

```http
POST /v1/queue
Content-Type: application/json

{"song_id":"SONG_ID"}
```

Adds a song manually. Manual additions may repeat the same song.

Returns `400` for malformed JSON or a missing `song_id`, and `404` for an unknown song.
JSON bodies for this endpoint and `POST /v1/queue:play-next` are capped at 4 KiB; a larger
body gets `413 {"error":"request body too large"}`.

```http
DELETE /v1/queue/{queue_item_id}
```

Removes a queued item.

```http
POST /v1/queue:skip
```

Interrupts the current track on every connected player by broadcasting `skip_now`, and
returns the upcoming song as `{"song": {...}}` without dequeuing it; the player's `need_song`
request dequeues it. With an empty queue, `song` is `null` and the player auto-selects, except
under `manual_only`, which returns `409 {"error":"queue is empty"}` and broadcasts nothing.

```http
POST /v1/queue:clear
```

Clears all pending queue entries.

```http
POST /v1/queue:play-next
Content-Type: application/json

{"song_id":"SONG_ID"}
```

Inserts a song at the front of the queue and broadcasts `skip_now`, so connected audio
clients abandon the current track and pull this one immediately.

Queue mutations broadcast `queue_updated` to connected WebSocket clients.

## ▶️ Player

```http
GET /v1/player
```

Returns the authoritative playback snapshot:

```json
{
  "current_song": { "id": "...", "title": "...", "artist": "..." },
  "queue_length": 0,
  "total_songs": 0
}
```

`current_song` is omitted while nothing is playing.

```http
POST /v1/player:skip
```

Broadcasts `skip_now` to every connected audio client. No server-side queue manipulation
happens here; the usual `need_song` flow performs the dequeue.

## 🕰️ History

```http
GET /v1/history?limit=20
```

Returns the most recent song starts, newest first, in the same `items`/`total` shape as
`GET /v1/queue`. `limit` defaults to `20`, values above `50` are clamped to `50`, and a
non-integer or value below `1` returns `400 {"error":"limit must be a positive integer"}`.

```json
{
  "items": [
    {
      "song": { "id": "...", "title": "...", "artist": "..." },
      "started_at": "2026-01-01T20:15:04Z"
    }
  ],
  "total": 1
}
```

History is kept in memory: it holds the last 50 starts and resets when the server
restarts. A song is recorded when a player starts it, so a skipped track still appears.

This list is separate from the Bloom filter, which is used only to decide what has
already been played this cycle. The `history_id` in WebSocket payloads is a per-playback
correlation ID, not a key into this list.
