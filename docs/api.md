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
HEAD /health
```

Returns `200` when the HTTP process is alive. `HEAD` returns the same status without a body,
for probes such as `wget --spider`.

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

Returns a song resource. `has_cover` is `true` when the file embeds a usable ID3v2 cover
image, so clients can skip the cover request for songs without art.

```http
GET /v1/songs/{id}/content
GET /v1/songs/{id}:stream
```

Streams MP3 content. The backend uses `http.ServeContent`, so range requests are supported by the standard library.

Returns `403` when the song path resolves outside the music directory, `404` when the file is
missing or is not a regular file, `416` for a range past the end of the file, and `500` when the
file cannot be read (for example, a permission error).

```http
GET /v1/songs/{id}/cover
```

Returns the embedded ID3v2 (APIC) cover image: the front cover when present, else the first
other picture. The type is sniffed from the bytes, and only `image/jpeg`, `image/png`,
`image/gif` and `image/webp` are served, with `X-Content-Type-Options: nosniff`. Responses carry
`Cache-Control: public, no-cache` and an `ETag`: clients revalidate on each use, a matching
`If-None-Match` returns `304`, and a retagged cover appears on the next request.

Returns `404` for an unknown song, a missing file, or a file without a usable picture (tags over
8 MiB are not parsed and count as no picture), `403` when the song path resolves outside the
music directory, and `500` when the file cannot be read.

### 🚫 Don't play this

```http
POST /v1/songs/{id}:block
POST /v1/songs/{id}:unblock
GET  /v1/blocked
```

`:block` pulls a song out of rotation: auto-select never picks it, its queued entries are
removed, and `POST /v1/queue` or `POST /v1/queue:play-next` for it returns
`409 {"error":"song is blocked"}`. The song playing now keeps playing. Blocking a blocked song
returns `200` again; an unknown song returns `404`.

`:unblock` puts the song back in rotation. It also clears a blocked ID whose file has left the
library, and returns `404` only when the ID is neither blocked nor in the library.

Both broadcast `queue_updated` with reason `song_blocked` or `song_unblocked`, and both return
`500` when the blocklist file cannot be saved (the change is rolled back).

`GET /v1/blocked` lists blocked songs sorted by ID. A song whose file has left the library is
listed by ID:

```json
{
  "songs": [
    { "id": "...", "title": "...", "artist": "..." },
    { "id": "...", "missing": true }
  ]
}
```

Blocked IDs are saved next to the Bloom file, with its extension replaced by `.blocked.json`
(`./lofi-radio.bloom` gives `./lofi-radio.blocked.json`), as
`{"version":1,"song_ids":[...]}`. Every change is written atomically, so the list survives a
restart. A corrupt file is logged at startup and replaced on the next change. Song IDs are
derived from the file path, so moving or renaming a file unblocks it.

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

Returns `400` for malformed JSON or a missing `song_id`, `404` for an unknown song, and
`409 {"error":"song is blocked"}` for a [blocked](#-dont-play-this) song.
JSON bodies for this endpoint and `POST /v1/queue:play-next` are capped at 4 KiB; a larger
body gets `413 {"error":"request body too large"}`.

```http
DELETE /v1/queue/{queue_item_id}
```

Removes a queued item.
Returns `204` with no body, `404` for an unknown item, and `500` if the queue store fails.

```http
POST /v1/queue/{queue_item_id}:move
Content-Type: application/json

{"direction":"up"}
```

Moves a queued item one place toward the front (`up`) or back (`down`) and returns
`{"id":"...","position":1,"moved":true}`. At either end of the queue it is a no-op with
`moved: false` and no broadcast. Returns `400` for any other direction, `404` for an
unknown item, and `413` for a body over 4 KiB.

```http
POST /v1/queue:skip
```

Interrupts the current track on every connected player by broadcasting `skip_now`, and
returns the upcoming song as `{"song": {...}}` without dequeuing it (queued entries whose file
left the library are passed over, as at play time); the player's `need_song`
request dequeues it. With no playable entry, `song` is `null` and the player auto-selects, except
under `manual_only`, which returns `409 {"error":"queue is empty"}` and broadcasts nothing. An
auto strategy with an empty library returns `409 {"error":"no songs available"}` and broadcasts
nothing as well.

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
clients abandon the current track and pull this one immediately. Auto-queued entries for
the same song are removed so it does not play twice; manually queued entries stay.
Like `POST /v1/queue`, it returns `404` for an unknown song and `409` for a blocked one.

Queue mutations broadcast `queue_updated` to connected WebSocket clients.

## ▶️ Player

```http
GET /v1/player
```

Returns the authoritative playback snapshot:

```json
{
  "current_song": { "id": "...", "title": "...", "artist": "..." },
  "elapsed_secs": 42.5,
  "queue_length": 0,
  "total_songs": 0
}
```

`elapsed_secs` is present exactly when `current_song` is: seconds since that song started,
computed by the server when the response is built and rounded to the millisecond. A retag
does not reset it. It is approximate (measured from the `play_song` dispatch, not the audible
start) and may exceed `duration_secs` while the next song is pending, so clients clamp it.

`total_songs` counts indexed songs in rotation; blocked songs are not counted. It matches the
websocket `state` frame.

`current_song` is omitted while nothing is playing. It is also cleared once a song finishes
with nothing queued under `manual_only`, or with an empty library under an auto strategy
(about 4s after the player reports it finished, once its crossfade tail has played out,
unless a new song starts first); `/v1/now-playing.txt` then
returns an empty body and the OBS now-playing file is emptied. The same happens about 5s
after the last WebSocket player connection closes, unless a player reconnects inside that
window (see [WebSocket protocol](websocket-protocol.md#state)); a silent network drop counts
only after the 60s pong timeout. A graceful server shutdown clears it and empties the OBS
file; a crash or `SIGKILL` leaves the file as is until the next start, which empties it.

```http
POST /v1/player:skip
```

Broadcasts `skip_now` to every connected audio client and returns
`{"message":"skip broadcast"}`. No server-side queue manipulation happens here; the usual
`need_song` flow performs the dequeue. Like `queue:skip`, under `manual_only` with no playable
queued entry it returns `409 {"error":"queue is empty"}`, and under an auto strategy with no
playable entry and an empty library it returns `409 {"error":"no songs available"}`; neither
broadcasts. A queue or library read failure returns `500` and broadcasts nothing.

```http
GET /v1/now-playing.txt
```

Returns the current song as one plain-text line (`text/plain; charset=utf-8`) for chat
bots. It uses `obs.now_playing_format` (default `{artist} - {title}`), so it matches the
OBS now-playing file. A song with no artist returns just its title, control characters in
tags become spaces, and there is no trailing newline. While nothing is playing it returns
`200` with an empty body, or with the optional `?idle=` text. Responses carry
`Cache-Control: no-store`.

`?idle=` is used only while nothing is playing. Control characters in it (including CR, LF,
tab and Unicode line/paragraph separators) become spaces and surrounding whitespace is
trimmed. It may be at most 200 characters; a longer value returns
`400 {"error":"idle must be at most 200 characters"}`, even while a song plays.

Chat bots and overlays must call the backend address (`BASE_URL`, port `8080` by
default), not the frontend. The frontend port answers every `/v1/*` path and `/ws` with
`404 {"error":"not_found","detail":"..."}` instead of the player page.

Chat command examples (replace the host with your public backend URL):

```text
Nightbot:      !song  ->  Now playing: $(urlfetch https://radio.example.com/v1/now-playing.txt)
StreamElements: !song  ->  Now playing: ${customapi.https://radio.example.com/v1/now-playing.txt}
```

These commands prefix `Now playing: `, so idle text appears after it. With
`https://radio.example.com/v1/now-playing.txt?idle=nothing%20right%20now` an idle radio
answers `Now playing: nothing right now`.

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

Add `format=text` for a plain-text setlist, meant for music attribution in a VOD
description. It lists the same entries oldest first, one UTC line each, as
`text/plain` with `Cache-Control: no-store`. A song without an artist shows the title
alone, and an untitled song shows its filename. Any other `format` returns
`400 {"error":"format must be json or text"}`. The admin **Copy setlist** button uses it.

```bash
curl 'http://localhost:8080/v1/history?format=text&limit=50'
2026-01-01 20:12 UTC  Terminal Club - Buffering dreams
2026-01-01 20:15 UTC  Nujabes - Aruarian Dance
```

History is kept in memory: it holds the last 50 starts and resets when the server
restarts. A song is recorded when a player starts it, so a skipped track still appears.

This list is separate from the Bloom filter, which is used only to decide what has
already been played this cycle. The `history_id` in WebSocket payloads is a per-playback
correlation ID, not a key into this list.
