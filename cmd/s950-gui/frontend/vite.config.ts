import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import path from 'node:path';

// Wails frontend config. Vitest reads the same config file for unit
// tests — the `test` block sets up jsdom + global describe/it/expect
// so tests don't have to import them everywhere.
//
// Tests live next to the code they cover, named `*.test.ts`. The
// Svelte plugin is registered for both build and test so component
// tests can import .svelte files directly.

// MOCK_WAILS=1 swaps the wailsjs Go-binding + runtime imports for the
// dev-only fixtures under scripts/screenshots/mocks/. Used by the
// headless-Chrome screenshot driver so views render with realistic
// content (long sample names, full keygroup tables, drawn waveforms)
// without a Wails build or a connected S950. Production unaffected.
const MOCK = process.env.MOCK_WAILS === '1';
const mockAliases = MOCK
  ? [
      {
        find: /(?:.*\/)?wailsjs\/go\/main\/App$/,
        replacement: path.resolve(__dirname, 'scripts/screenshots/mocks/App.ts'),
      },
      {
        find: /(?:.*\/)?wailsjs\/runtime\/runtime$/,
        replacement: path.resolve(__dirname, 'scripts/screenshots/mocks/runtime.ts'),
      },
    ]
  : [];

/// <reference types="vitest" />
export default defineConfig({
  plugins: [svelte({ hot: false })],
  resolve: { alias: mockAliases },
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
