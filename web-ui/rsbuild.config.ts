import { defineConfig } from '@rsbuild/core';
import { pluginReact } from '@rsbuild/plugin-react';
import http from 'node:http';

// Create an HTTP/1-only agent for the proxy
const httpAgent = new http.Agent({
  keepAlive: false,
});

export default defineConfig({
  plugins: [pluginReact()],
  html: {
    title: 'SDS UI',
    favicon: './src/assets/favicon.svg',
  },
  resolve: {
    alias: {
      '@': './src',
    },
  },
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
    // The SPA calls the controller's REST gateway at /v1/* directly and the
    // Copilot at /ai/* (served from the controller's UI port), so a dev
    // session proxies both prefixes to the cluster VIP and sees real data.
    proxy: {
      '/v1': {
        target: 'http://192.168.123.251:3375',
        changeOrigin: true,
        agent: httpAgent,
        // Force HTTP/1.1
        protocol: 'http',
      },
      '/ai': {
        target: 'http://192.168.123.251:3376',
        changeOrigin: true,
        agent: httpAgent,
        protocol: 'http',
      },
    },
  },
});
