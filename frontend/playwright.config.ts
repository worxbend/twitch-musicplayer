import { defineConfig } from '@playwright/test';

// PW_PROD=1 runs the @smoke subset against the production bundle served by
// vite preview, so breakage that only shows up in a build is caught.
const prod = process.env.PW_PROD === '1';
const devServer = 'npm run dev -- --host 127.0.0.1 --port 3100 --strictPort';
const prodServer = 'npx vite build --outDir dist-e2e && npx vite preview --outDir dist-e2e --host 127.0.0.1 --port 3100 --strictPort';

export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['github'], ['html', { open: 'never' }]] : 'list',
  workers: 1,
  grep: prod ? /@smoke/ : undefined,
  // A test may scan four pages with axe or capture software-rendered WebGL.
  timeout: 60000,
  use: { baseURL: 'http://127.0.0.1:3100', viewport: { width: 1440, height: 960 }, trace: 'retain-on-failure' },
  webServer: {
    command: prod ? prodServer : devServer,
    url: 'http://127.0.0.1:3100',
    env: { VITE_BACKEND_URL: 'http://127.0.0.1:18089' },
    reuseExistingServer: false,
    // The production run builds the bundle before it serves it.
    timeout: prod ? 180000 : 60000,
  },
});
