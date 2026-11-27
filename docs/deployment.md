<div align="center">

<a href="../README.md"><img src="../frontend/src/assets/worxbend-logo.png" width="72" alt="Lofi Radio" /></a>

# 🚀 Deployment Guide

### *Getting the station on the air, and keeping it there.*

[🏠 Home](../README.md) · [🗺️ Overview](overview.md) · [🎧 User](user-guide.md) · [🛠️ Dev](developer-guide.md) · [🌐 API](api.md) · [🔌 WebSocket](websocket-protocol.md) · [⚙️ Config](configuration.md) · **🚀 Deploy**

</div>

---

## 🏗️ Deployment Shape

Two services, either way you run them:

- **Backend** — a single static Go binary plus a mounted MP3 folder. No database service.
- **Frontend** — static assets built by Vite, served by nginx or any static host.

The fastest path is Docker Compose. Building from source is documented further down.

---

## 🐳 Docker Compose

Three compose files, for three different jobs.

| File | Job | Images |
|:--|:--|:--|
| `docker-compose.yml` | Run it locally | Built from the Dockerfiles in this repo |
| `docker-compose.build.yml` | Build and publish | Tagged for Docker Hub |
| `docker-compose.hub.yml` | Run it anywhere | Pulled from Docker Hub |

### 🚀 Run locally

```bash
cp .env.example .env          # optional: set MUSIC_DIR and ports
docker compose up -d --build
```

| | URL |
|:--|:--|
| 🎵 Visualizer | http://localhost:3000 |
| 🎚️ Admin console | http://localhost:3000/admin |
| 📖 Swagger | http://localhost:8080/swagger |

Stop with `docker compose down`. Add `-v` to also discard the play history.

### 📦 Run from Docker Hub

No source checkout needed — one file is enough:

```bash
curl -O https://raw.githubusercontent.com/worxbend/twitch-musicplayer/main/docker-compose.hub.yml
MUSIC_DIR=~/Music docker compose -f docker-compose.hub.yml up -d
```

### 🏗️ Build and publish

```bash
docker login
IMAGE_TAG=0.1.0 docker compose -f docker-compose.build.yml build
IMAGE_TAG=0.1.0 docker compose -f docker-compose.build.yml push
```

That builds for the current machine's architecture. For images that also run on
Apple Silicon and Raspberry Pi, register the emulators once and use Buildx Bake:

```bash
docker run --privileged --rm tonistiigi/binfmt --install all   # once
docker buildx create --use --name lofi                          # once
IMAGE_TAG=0.1.0 docker buildx bake -f docker-compose.build.yml \
    --set '*.platform=linux/amd64,linux/arm64' --push
```

> [!NOTE]
> The platform list is not baked into the compose file on purpose. Without the
> emulators installed, a plain `docker compose build` would fail with
> `exec format error` on the foreign architecture.

### ⚙️ Compose settings

Every value has a working default, so `docker compose up -d --build` runs with no `.env`
at all.

| Variable | Default | What it does |
|:--|:--|:--|
| `MUSIC_DIR` | `./music` | Host folder mounted read-only at `/music` |
| `FRONTEND_PORT` | `3000` | Host port for the visualizer |
| `BACKEND_PORT` | `8080` | Host port for the API and WebSocket |
| `BASE_URL` | `http://localhost:8080` | Address the backend puts into stream URLs |
| `BACKEND_URL` | *(empty)* | Address the page looks for the backend at |
| `ALLOWED_ORIGINS` | `http://localhost:<FRONTEND_PORT>,http://127.0.0.1:<FRONTEND_PORT>` | Frontend addresses whose pages may control the station |
| `VITE_BACKEND_URL` | *(empty)* | Compiles a backend URL into the bundle |
| `VITE_AUTO_START_AUDIO` | `false` | Start audio without a click |
| `DOCKERHUB_NAMESPACE` | `worxbend` | Docker Hub account for build/hub files |
| `IMAGE_TAG` | `latest` | Tag for build/hub files |

> [!IMPORTANT]
> `BASE_URL` and `BACKEND_URL` must be reachable **from the browser**, not from inside the
> Compose network. Using `http://backend:8080` looks tidy and does not work: the browser
> cannot resolve a Compose service name. If you move `BACKEND_PORT`, set both `BASE_URL`
> and `BACKEND_URL` to `http://<host>:<BACKEND_PORT>`. Otherwise the page falls back to
> port 8080 and the player stays Offline, and the stream URLs point at the wrong port.

### 🌍 Deploying to a real host

The published frontend image carries **no** backend URL. It resolves one at container
start, in this order:

1. `BACKEND_URL`, written into `/config.js` by the image's entrypoint.
2. `VITE_BACKEND_URL`, if the image was built with one.
3. The host that served the page, on port 8080.

Step 3 is why `BACKEND_URL` is optional on a remote server: browse to
`http://myserver:3000` and the page talks to `http://myserver:8080`. `BASE_URL` is not
optional there. It defaults to `http://localhost:8080`, so without
`BASE_URL=http://myserver:8080` every stream URL points at the viewer's own machine: the
badge shows Live and nothing plays. Set `BACKEND_URL` too when the backend is somewhere
else, on another port, or behind TLS:

```bash
BASE_URL=https://radio.example.com \
BACKEND_URL=https://radio.example.com \
docker compose -f docker-compose.hub.yml up -d
```

`ALLOWED_ORIGINS` is not optional on a remote server either: set it to the address you
open the frontend at (`ALLOWED_ORIGINS=http://myserver:3000`, or
`https://radio.example.com` above). Otherwise the backend refuses the page's WebSocket
with `403` and the player stays Offline.

Changing it needs only a restart, never a rebuild. 🎉

`BACKEND_URL` must be an absolute `http://` or `https://` URL with no spaces, quotes,
angle brackets or control characters. Any other value stops the container at start
with `lofi-config: invalid BACKEND_URL`.

### 💾 What persists

The `bloom-data` named volume holds the play history, so the rotation survives a restart.
It also holds the blocklist (`lofi-radio.blocked.json`), so keep it persistent and include it
in backups.
Your music is mounted read-only and is never written to. Delete the volume with
`docker compose down -v` to start the rotation fresh.

### 🔒 Container hardening

- The frontend image runs nginx as the unprivileged `nginx` user (uid 101) on container
  port 80. Binding port 80 without root relies on Docker's default
  `net.ipv4.ip_unprivileged_port_start=0` sysctl in the container. Under host networking
  or a stricter runtime, publish another port or grant `NET_BIND_SERVICE`.
- The backend service in the compose files runs with a read-only root filesystem, a
  `/tmp` tmpfs, no capabilities and `no-new-privileges`. It writes only to the `/data`
  volume, so a custom `obs.now_playing_file` must point into a writable mounted directory.

---

## 🎛️ Backend Build

```bash
cd backend
go build -o lofi-radio-backend ./cmd/api
```

Run:

```bash
PORT=8080 \
BLOOM_PATH=/data/lofi-radio.bloom \
MUSIC_DIR=/data/music \
BASE_URL=https://radio.example.com \
./lofi-radio-backend
```

## 🖼️ Frontend Build

```bash
cd frontend
VITE_BACKEND_URL=https://radio.example.com npm run build
```

Serve `frontend/dist` from a static server.

## 🔀 Reverse Proxy

A typical proxy should route:

- `/` to the frontend static app.
- `/v1/*` to the backend.
- `/ws` to the backend with WebSocket upgrade support.
- `/health` to the backend.

Example Nginx sketch:

```nginx
server {
  listen 443 ssl;
  server_name radio.example.com;

  root /srv/lofi/frontend;
  index index.html;

  # HTML must revalidate, or a cached page after a redeploy points at
  # hashed /assets/ files that no longer exist.
  location = /index.html {
    add_header Cache-Control "no-cache";
    try_files $uri =404;
  }

  location = /admin {
    add_header Cache-Control "no-cache";
    try_files /admin.html =404;
  }

  location / {
    try_files $uri /index.html;
  }

  location /v1/ {
    proxy_pass http://127.0.0.1:8080;
  }

  location /health {
    proxy_pass http://127.0.0.1:8080;
  }

  location /ws {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
  }
}
```

## 💾 Persistent Storage

Persist:

- Bloom-filter history path from `BLOOM_PATH` (defaults to `./lofi-radio.bloom`). Mount it on a persistent volume so play history survives restarts. The blocklist is saved in the same directory as `<name>.blocked.json`; keep it on the same volume and include it in backups.
- Music folder from `MUSIC_DIR`.

Do not place these inside ephemeral container layers.

## 🔐 Security Notes

There is no authentication. Only browser origins in `ALLOWED_ORIGINS` may open `/ws`,
read API responses or send writes; pages from other sites get `403`. This matters even on
a localhost-only install: any web page open in the streamer's browser, such as a link
clicked in chat, can reach `http://localhost:8080`. Keep the list to your own frontend
addresses, and never add a site you do not control. Clients without an `Origin` header,
such as curl or anything on the LAN that can reach port 8080, are not restricted. For
production:

- Do not publish port 8080 beyond the machines that need it.
- Add authentication for queue control endpoints if exposed publicly.
- Consider read-only public song streaming and authenticated queue mutation.

## 🛠️ Operational Notes

- The backend scans the music folder, including nested folders, on startup. The scan finishes before the API listens, so startup takes longer as the library grows. The healthcheck allows 300s for it; raise `start_period` for a very large library on slow storage.
- New files are indexed and queued once their size stops changing, so a slow copy straight into the folder is safe.
- Deleted or renamed files are dropped from the library and the queue.
- Symlinks that resolve outside `music_dir` are skipped.
