import { defineConfig } from '@rsbuild/core';
import { pluginReact } from '@rsbuild/plugin-react';
import http from 'node:http';

// Create an HTTP/1-only agent for the proxy
const httpAgent = new http.Agent({
  keepAlive: false,
});

export default defineConfig({
  plugins: [pluginReact()],
  source: {
    entry: {
      index: './src/index.tsx',
    },
  },
  output: {
    distPath: {
      root: './dist',
    },
  },
  server: {
    port: 3000,
    proxy: {
      '/api': {
        target: 'http://orange1:3375',
        changeOrigin: true,
        pathRewrite: {
          '^/api': '/v1',
        },
        agent: httpAgent,
        // Force HTTP/1.1
        protocol: 'http',
      },
    },
  },
});
