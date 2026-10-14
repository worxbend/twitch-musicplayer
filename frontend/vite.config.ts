import { defineConfig, type ViteDevServer } from 'vite';
import solidPlugin from 'vite-plugin-solid';
import { resolve } from 'path';

// Match the production nginx routes during local development and preview.
function localRoutes(server: Pick<ViteDevServer, 'middlewares'>) {
  server.middlewares.use((req, res, next) => {
    const [path, query] = (req.url ?? '').split('?');
    if (path === '/admin' || path === '/admin/') req.url = `/admin.html${query ? `?${query}` : ''}`;
    if (path === '/config.js') {
      res.setHeader('Content-Type', 'application/javascript');
      res.end('window.__LOFI_CONFIG__ = window.__LOFI_CONFIG__ || {};');
      return;
    }
    next();
  });
}

export default defineConfig({
  plugins: [solidPlugin(), { name: 'local-app-routes', configureServer: localRoutes, configurePreviewServer: localRoutes }],
  server: { port: 3000 },
  build: {
    target: 'esnext',
    rollupOptions: {
      input: {
        main:  resolve(__dirname, 'index.html'),
        admin: resolve(__dirname, 'admin.html'),
      },
    },
  },
});
