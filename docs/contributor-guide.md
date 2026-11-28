<div align="center">

<a href="../README.md"><img src="../frontend/src/assets/worxbend-logo.png" width="72" alt="Lofi Radio" /></a>

# 🤝 Contributor Guide

### *Conventions and expectations before you open a pull request.*

[🏠 Home](../README.md) · [🗺️ Overview](overview.md) · [🎧 User](user-guide.md) · [🛠️ Dev](developer-guide.md) · [🌐 API](api.md) · [🔌 WebSocket](websocket-protocol.md) · [⚙️ Config](configuration.md) · [🚀 Deploy](deployment.md)

</div>

---

## 🧭 Development Principles

- Keep backend playback behavior authoritative and explicit.
- Keep frontend playback backend-only.
- Prefer resource-oriented HTTP endpoints under `/v1`.
- Prefer browser-native media streaming over fetching audio bytes into JavaScript.
- Add focused tests for queue, history, repository, and protocol behavior when changing backend semantics.
- Avoid visible UI controls that conflict with the current passive visualizer experience.

## 🌿 Branch Workflow

1. Create a feature branch.
2. Keep backend and frontend changes scoped.
3. Run the checks in the PR checklist below.
4. Update docs when behavior or configuration changes.
5. Include known limitations in the PR description if a requirement is partially implemented.

## ✅ Suggested PR Checklist

- `make check` passes at the repo root (toolchains, lint, backend and frontend gates). If make reports npm not found, put Node on PATH first, e.g. `nvm use` in `frontend/` or `PATH=~/.nvm/versions/node/<version>/bin:$PATH make check`.
- `make ui` passes (run `npx playwright install chromium` in `frontend/` once first).
- `make docker-smoke` passes when a Dockerfile, the nginx config or a compose file changes.
- New configuration has examples in `backend/config.example.toml`, `backend/.env.example`, or `frontend/.env.example`.
- WebSocket payload shape changes are reflected in `docs/websocket-protocol.md`.
- HTTP endpoint changes are reflected in `docs/api.md`.
- User-visible workflow changes are reflected in `docs/user-guide.md`.

## 🧪 Testing Gaps To Close

High-value tests to add:

- In-memory queue repository: `Remove` of a missing id returns `sql.ErrNoRows` (only the API handler tests cover the 404 today, with a fake repository).
- WebSocket: one end-to-end loop over a real socket, `need_song -> play_song -> song_finished -> play_song` (client tests cover each step separately).

## 🔊 Supported Playback Model

The supported playback model is one active WebSocket client. Do not add frontend flows that create competing playback clients unless the backend protocol is intentionally redesigned.
