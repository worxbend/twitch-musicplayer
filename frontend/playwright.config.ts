import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  workers: 1,
  // A test may scan four pages with axe or capture software-rendered WebGL.
  timeout: 60000,
  use: { baseURL: 'http://127.0.0.1:3100', viewport: { width: 1440, height: 960 }, trace: 'retain-on-failure' },
  webServer: {
    command: 'npm run dev -- --host 127.0.0.1 --port 3100 --strictPort',
    url: 'http://127.0.0.1:3100',
    env: { VITE_BACKEND_URL: 'http://127.0.0.1:18089' },
    reuseExistingServer: false,
  },
});
