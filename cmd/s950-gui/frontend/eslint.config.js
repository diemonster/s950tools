// Flat-config ESLint for the Wails Svelte 3 + TypeScript frontend.
// Layered: JS recommended → typescript-eslint recommended → Svelte
// recommended. Wails-generated bindings and the Vite build output are
// ignored. svelte-check already handles type errors, so this config
// focuses on stylistic + correctness lint rules.

import js from '@eslint/js';
import ts from 'typescript-eslint';
import svelte from 'eslint-plugin-svelte';
import globals from 'globals';

export default [
  js.configs.recommended,
  ...ts.configs.recommended,
  ...svelte.configs['flat/recommended'],
  {
    languageOptions: {
      globals: { ...globals.browser, ...globals.node },
    },
    rules: {
      // Allow `_unused` parameter convention (drag callbacks etc.).
      // caughtErrors: 'none' exempts catch parameters — we type them
      // `(e: any)` for the `String(e?.message ?? e)` idiom and the
      // catch may legitimately ignore the variable.
      '@typescript-eslint/no-unused-vars': ['warn', {
        argsIgnorePattern: '^_',
        varsIgnorePattern: '^_',
        caughtErrors: 'none',
      }],
      // The frontend interops with untyped Wails bindings (App as any);
      // forbidding `any` outright would force us to hand-roll types
      // that wailsjs already exports. Downgrade to warning.
      '@typescript-eslint/no-explicit-any': 'warn',
      // Catch handlers commonly ignore the error (try/catch around
      // best-effort host calls). Don't require a name.
      'no-empty': ['error', { allowEmptyCatch: true }],
    },
  },
  {
    files: ['**/*.svelte'],
    languageOptions: {
      parserOptions: { parser: ts.parser },
    },
    rules: {
      // Svelte's a11y heuristics overlap with what svelte-check
      // already surfaces — we audited those manually in a recent
      // pass. Keep the rules on but as warnings.
      'svelte/no-at-html-tags': 'warn',
    },
  },
  {
    ignores: [
      'dist/**',
      'wailsjs/**',
      'node_modules/**',
      'build/**',
    ],
  },
];
