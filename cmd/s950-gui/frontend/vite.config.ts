import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// Wails frontend config. Vitest reads the same config file for unit
// tests — the `test` block sets up jsdom + global describe/it/expect
// so tests don't have to import them everywhere.
//
// Tests live next to the code they cover, named `*.test.ts`. The
// Svelte plugin is registered for both build and test so component
// tests can import .svelte files directly.

/// <reference types="vitest" />
export default defineConfig({
  plugins: [svelte({ hot: false })],
  test: {
    globals: true,
    environment: 'jsdom',
    include: ['src/**/*.test.ts'],
    setupFiles: ['src/test/setup.ts'],
    // Wails bindings + dist/build outputs should never be loaded by
    // test discovery — they aren't real source.
    exclude: ['node_modules', 'dist', 'build', 'wailsjs'],
  },
});
