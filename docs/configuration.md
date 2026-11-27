<div align="center">

<a href="../README.md"><img src="../frontend/src/assets/worxbend-logo.png" width="72" alt="Lofi Radio" /></a>

# ⚙️ Configuration

### *Every TOML key and environment override, and what they change.*

[🏠 Home](../README.md) · [🗺️ Overview](overview.md) · [🎧 User](user-guide.md) · [🛠️ Dev](developer-guide.md) · [🌐 API](api.md) · [🔌 WebSocket](websocket-protocol.md) · **⚙️ Config** · [🚀 Deploy](deployment.md)

</div>

---

## 📄 Backend TOML

Example: `backend/config.example.toml`.

```toml
[server]
port = 8080
base_url = "http://localhost:8080"

[bloom]
path = "./lofi-radio.bloom"  # play history: flushed every minute when it changed, and on clean shutdown

[music]
dir = "./music"
extensions = [".mp3", ".MP3"]

[shuffle]
strategy = "round_robin"
recent_window = 0

[queue]
strategy = "auto_refill"
min_ahead = 1
preload_size = 3
```

Set `LOFI_CONFIG` to use a custom TOML file:

```bash
LOFI_CONFIG=/etc/lofi/config.toml ./lofi-radio-backend
```

When `LOFI_CONFIG` is set, the file must exist and be readable, or startup fails. Without it,
a missing `config.toml` means built-in defaults.

## 🌱 Backend Environment

Example: `backend/.env.example`.

```bash
PORT=8080
APP_ENV=local
BLOOM_PATH=./lofi-radio.bloom
MUSIC_DIR=./music
BASE_URL=http://localhost:8080
ALLOWED_ORIGINS=http://localhost:3000,http://127.0.0.1:3000
```

Environment variables override TOML values:

| Variable | Overrides | Default |
|:--|:--|:--|
| `LOFI_CONFIG` | path to the TOML file itself | `config.toml` |
| `PORT` | `server.port` | `8080` |
| `BASE_URL` | `server.base_url` | `http://localhost:8080` |
| `ALLOWED_ORIGINS` | `server.allowed_origins` (comma-separated) | `http://localhost:3000,http://127.0.0.1:3000` |
| `MUSIC_DIR` | `music.dir` | `./music` |
| `BLOOM_PATH` | `bloom.path` | `./lofi-radio.bloom` |

`APP_ENV` appears in `.env.example` for convention but is not read by the service.
Shuffle and queue settings are TOML-only — there is no environment override for them.

## ✅ Validation

The backend checks its config at startup. If anything is wrong, it exits non-zero
and prints every problem it found, each one naming its TOML key. A missing
`config.toml` is fine: defaults are used. These fail startup:

- A `LOFI_CONFIG` file that is missing, or a config file that cannot be read.
- A TOML file that does not parse.
- Unknown keys or sections (a typo such as `queue.min_ahed`); the error names each one.
- A `PORT` that is not an integer, or a `server.port` outside `1..65535`.
- An unknown `shuffle.strategy`. Allowed: `random`, `weighted_history`, `round_robin`, `least_played`.
- An unknown `queue.strategy`. Allowed: `manual_only`, `auto_refill`, `preload`.
- A negative `shuffle.recent_window`, `queue.min_ahead` or `queue.preload_size`.
- An empty `music.dir`, or an empty or blank-entry `music.extensions` list.
- An `obs.now_playing_format` with neither `{title}` nor `{artist}` (always checked), or an
  `obs.now_playing_file` whose directory does not exist or that is itself a directory
  (checked only when the file is set).
- A `bloom.path` (or `BLOOM_PATH`) whose directory does not exist, or that names an existing
  directory. A missing file is fine; it is created on the first save.
- A `server.base_url` (or `BASE_URL`) that is not an `http://` or `https://` URL with a host,
  or that has a `?query` or `#fragment`. Empty is allowed: stream URLs then stay relative and
  the frontend resolves them against `VITE_BACKEND_URL`.

- A `server.allowed_origins` (or `ALLOWED_ORIGINS`) entry that is not exactly
  `http(s)://host[:port]`: no path, query, fragment or user info, and no `*` wildcard.

Extensions without a leading dot are accepted and normalized (`"mp3"` becomes `".mp3"`).
Trailing slashes on `server.base_url` are trimmed (`http://host:8080/` becomes `http://host:8080`).
Origins are lowercased and lose a trailing slash; blank entries in `ALLOWED_ORIGINS` are dropped.

## 🔒 Allowed Origins

`server.allowed_origins` lists the frontend addresses whose pages may open `/ws`, read API
responses and send writes. A page from any other site gets `403`, and no
`Access-Control-Allow-Origin`. Requests without an `Origin` header (curl, scripts, OBS
plugins) are not affected, and the `server.base_url` origin is always allowed so
`/swagger` keeps working. Opening the frontend at another address, such as
`http://myserver:3000` or a moved port, needs that address added.

## 📝 OBS Now-Playing File

Optional. The backend writes the current song to a plain text file, so an OBS
**Text (GDI+/FreeType)** source can show it without a browser source. Off by default.

```toml
[obs]
now_playing_file = "~/obs/nowplaying.txt"
now_playing_format = "{artist} - {title}"
```

| Key | Default | Meaning |
|:--|:--|:--|
| `now_playing_file` | `""` (off) | File to rewrite on every song change and when the current song is retagged. `~` expands to your home. The directory must exist, and the path must not be a directory itself. |
| `now_playing_format` | `{artist} - {title}` | Text template. Placeholders: `{title}`, `{artist}`, `{album}`. Also drives [`GET /v1/now-playing.txt`](api.md), so it is validated even when `now_playing_file` is empty. |

- A song with no artist tag writes just its title, so no stray ` - ` appears. Line breaks
  in tags become spaces, so the text stays on one line.
- Each write goes to a temp file in the same directory and is renamed over the target,
  so OBS never reads a half-written title.
- The file is emptied at startup, so a title from the last run does not linger.
- A failed write is logged and skipped; playback is unaffected.

Set it up in OBS: **Sources → + → Text (GDI+)** (FreeType on Linux/macOS) → tick
**Read from file** → pick the file. OBS reloads it when it changes.

## 🖼️ Frontend Environment

Example: `frontend/.env.example`.

```bash
VITE_BACKEND_URL=http://localhost:8080
VITE_AUTO_START_AUDIO=false
```

`VITE_BACKEND_URL` is used to derive:

- WebSocket URL: `ws://.../ws` or `wss://.../ws`. A path prefix is kept, so `https://host/radio` gives `wss://host/radio/ws`.
- Relative stream URL resolution.

`VITE_AUTO_START_AUDIO=true` lets the frontend try to start playback without a browser click. OBS browser sources are detected through the `obsstudio` global and always request autoplay, whatever this value says. `?autoplay=1` or `?autoplay=0` on any route overrides both.

## 📋 Queue Strategies

Controls **when** the queue is topped up. Set with `queue.strategy`.

| Strategy | Behaviour | Uses |
|:--|:--|:--|
| `auto_refill` | Selects automatically and keeps `min_ahead` songs queued ahead | `min_ahead` |
| `preload` | Keeps `preload_size` songs queued at all times | `preload_size` |
| `manual_only` | Plays only what was queued by hand; never auto-selects | — |

> [!WARNING]
> Under `manual_only` the station goes silent once the queue empties — the client receives
> a `queue is empty` error rather than a track. Use it only when something else is feeding
> the queue. 🤫

## 🔀 Shuffle Strategies

Controls **which** song is chosen. Set with `shuffle.strategy`.

| Strategy | Behaviour | Good for |
|:--|:--|:--|
| `round_robin` | Plays every song once before repeating any | **Default.** Fair rotation |
| `weighted_history` | Same cycle, but weights recently played songs down using `recent_window` | Large libraries |
| `least_played` | Prefers songs not yet played this cycle, falling back to the full pool | Fresh imports |
| `random` | Pure random; may repeat immediately | Chaos 😈 |

Every strategy except `random` also refuses to play two songs by the same artist
back-to-back, as long as an alternative exists.

`round_robin` is the default because it best matches the no-repeat playback requirement.

### 🎚️ Tuning `recent_window`

Only `weighted_history` reads it. A song played *N* selections ago receives weight
`(N+1)/(window+1)`, so the most recent track is the least likely to come back and anything
outside the window keeps full weight.

| Value | Meaning |
|:--|:--|
| `0` | **Automatic** — resolves to `min(library/2, 20)` |
| `n` | Down-weight the last `n` plays; clamped so it never covers the whole library |

> [!NOTE]
> "Played" is tracked in a Bloom filter, not a history table. Bloom filters have no false
> negatives but do have false positives, so a song is very occasionally treated as already
> played and skipped within a cycle. That is the cost of having no database. 🌸
