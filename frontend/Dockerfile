# ── Build stage ───────────────────────────────────────────────────────────────
FROM node:26-alpine AS builder

WORKDIR /app

COPY package.json package-lock.json ./
RUN npm ci

COPY . .

# Build-time configuration. VITE_BACKEND_URL is optional: leaving it empty
# produces an image that resolves the backend at runtime instead, either from
# the BACKEND_URL environment variable or from the host that served the page.
ARG VITE_BACKEND_URL=
ARG VITE_AUTO_START_AUDIO=false
ENV VITE_BACKEND_URL=$VITE_BACKEND_URL
ENV VITE_AUTO_START_AUDIO=$VITE_AUTO_START_AUDIO

RUN npm run build

# ── Runtime stage ─────────────────────────────────────────────────────────────
# The unprivileged image runs nginx as uid 101 with its pid and temp files
# under /tmp. Port 80 still binds without capabilities thanks to Docker's
# default net.ipv4.ip_unprivileged_port_start=0 inside the container.
FROM nginxinc/nginx-unprivileged:1.30-alpine

COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

# nginx runs every executable in this directory before starting. The script
# writes /tmp/lofi/config.js from the environment, so the backend URL can
# change without rebuilding the image and the whole web root stays root-owned
# and read-only.
COPY --chmod=755 docker-entrypoint.d/20-lofi-config.sh /docker-entrypoint.d/

USER 101

EXPOSE 80

HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1/ || exit 1
