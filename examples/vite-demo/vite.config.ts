import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react-swc';
import path from 'path';

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  build: {
    sourcemap: true,
  },
  server: {
    sourcemapIgnoreList: false,
    https: {
      key: path.resolve(__dirname, '../_certs/localhost+2-key.pem'),
      cert: path.resolve(__dirname, '../_certs/localhost+2.pem'),
    },
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
});
