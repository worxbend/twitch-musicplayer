# Broadcast terminal redesign

A terminal-inspired identity for a self-hosted radio: JetBrains Mono, charcoal surfaces, phosphor-mint controls, amber status signals, violet queue accents, and a repeating `▂▅█▅▂` mark. The UI borrows the precision of a command-line workspace without making you learn commands.

## What changed

- Shared navigation connects the player, scene directory, and station console.
- The player and all seven opaque scene pages share a framed broadcast workspace, current-track deck, progress indicator, and audio-unlock button.
- Scene cards use lightweight SVG signal diagrams. The colour-ramp menu updates diagrams, scene links, and copyable OBS URLs. The oscilloscope retains its fixed cyan/magenta colours.
- The console includes responsive library and queue panels, clear-queue confirmation, loading and empty states, recoverable request errors, and guarded search results.
- Shared tokens and buttons live in `frontend/src/styles/tokens.css`. The font is self-hosted with its OFL license.
- Reduced motion holds the scene composition still without stopping playback. The ambient breathing animation now uses the scene clock instead of a separate GSAP tween; GSAP is no longer required.
- Existing browser-source routes keep their clean, chrome-free presentation. Transparent routes keep a transparent background.

## Screenshots

Screenshots show an isolated local backend with generated test audio and sample track metadata, not a production library.

### Desktop · 1440 × 960 viewport

- [Ambient player](player-desktop.png)
- [Scene directory, full page](scenes-desktop.png)
- [Station console, full page](console-desktop.png)
- [Equaliser scene](equaliser-desktop.png)

### Mobile · 390 × 844 viewport

- [Ambient player](player-mobile.png)
- [Scene directory, opening viewport](scenes-mobile.png)
- [Station console, full page](console-mobile.png)

### Tablet · 768 × 1024 viewport

- [Scene directory, full page](scenes-tablet.png)

## Verification performed

- `cd frontend && npm run build`: passed TypeScript checking and production bundling.
- `cd frontend && npm test`: eight tests passed.
- `cd frontend && npm run test:ui`: 21 Chromium tests passed. The suite checks navigation, focus, queue actions, confirmations, errors, stale searches, clipboard fallback, audio unlock, reduced motion, all scene routes, and overlay transparency.
- Axe WCAG A/AA scans of the directory, console, ambient player, and equaliser at widths of 1440, 768, 390, and 320 pixels: no detected violations. No horizontal document overflow at those sizes. These automated checks don't replace a manual screen-reader review.
- Separate real-backend browser checks passed: WebSocket connection, keyboard audio unlock, streamed playback, search, queue add/remove, play-now, skip, clear/cancel, live metadata updates, and clipboard URLs. HTTP responses were 200, 201, 204, and 206; no application console errors. See [live verification results](live-verification.json).
- `cd backend && go test ./... -race`: passed.
- `git diff --check`: passed.

The production main entry measured 317.94 kB before gzip after removing GSAP, versus 388.23 kB in the first redesign build with GSAP. This is a bundle-size comparison, not an FPS or loading-speed benchmark.

### Repeat the browser tests

From `frontend`:

```sh
npm ci
npx playwright install chromium
npm run test:ui
```

Tests start an isolated Vite server on port 3100 and mock the backend. No music folder or live backend is required. The separate real-backend checks used temporary generated MP3 files, backend port 18081, and frontend port 3001.

## Remaining issues and boundaries

- `npm audit` reports 10 existing dependency findings: two critical, six high, and two moderate, including Solid/seroval and Vite. Dependency remediation needs a separate compatibility review; this redesign doesn't claim to resolve them.
- Vite retains its warning about the runtime `/config.js` script not being bundled. Docker generates that script; the local development and preview servers supply an empty fallback.
- Headless Chromium reported GPU `ReadPixels` performance warnings during screenshot capture. No JavaScript application errors occurred in the live checks.
- Actual OBS embedding, Firefox/Safari, and screen-reader testing weren't performed. Browser-source transparency and absence of UI chrome were verified in Chromium.
