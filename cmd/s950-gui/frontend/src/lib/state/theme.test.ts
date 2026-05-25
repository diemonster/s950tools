// Theme store tests. The visual swap (yellow / green / black surfaces
// in party mode) is CSS-driven via the `data-theme` attribute, which
// jsdom doesn't render meaningfully — so we test the *contract*
// instead: initial value sourced from localStorage, toggle cycle,
// and that both side effects (DOM attribute + localStorage write)
// fire on every change.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';

// LocalStorage key must match what theme.ts uses; if these drift,
// users lose their saved preference silently on a release. The
// constant is duplicated here on purpose so a rename in theme.ts
// fails the test instead of slipping past.
const STORAGE_KEY = 's950-tools.theme';

beforeEach(() => {
  // Reset between tests so initial-value reads aren't contaminated.
  localStorage.clear();
  document.documentElement.removeAttribute('data-theme');
  // Force a fresh module load so the initial readInitial() picks up
  // whatever localStorage state we set per-test.
  vi.resetModules();
});

describe('theme store — initial value', () => {
  it("defaults to 'business' when localStorage is empty", async () => {
    const { theme } = await import('./theme');
    expect(get(theme)).toBe('business');
  });

  it("reads 'party' from localStorage when present", async () => {
    localStorage.setItem(STORAGE_KEY, 'party');
    const { theme } = await import('./theme');
    expect(get(theme)).toBe('party');
  });

  it('falls back to business when localStorage holds garbage', async () => {
    localStorage.setItem(STORAGE_KEY, 'rainbow-mode');
    const { theme } = await import('./theme');
    // Unknown values are treated as business — defensive against
    // future renames or hand-edited preferences.
    expect(get(theme)).toBe('business');
  });
});

describe('theme store — toggleTheme', () => {
  it('cycles business → party → business', async () => {
    const { theme, toggleTheme } = await import('./theme');
    expect(get(theme)).toBe('business');
    toggleTheme();
    expect(get(theme)).toBe('party');
    toggleTheme();
    expect(get(theme)).toBe('business');
  });
});

describe('theme store — side effects', () => {
  it('mirrors state to the document data-theme attribute', async () => {
    const { toggleTheme } = await import('./theme');
    // Subscribing in theme.ts module init fires once with the
    // initial value, so the attribute should already be set.
    expect(document.documentElement.dataset.theme).toBe('business');
    toggleTheme();
    expect(document.documentElement.dataset.theme).toBe('party');
  });

  it('persists every change to localStorage', async () => {
    const { toggleTheme } = await import('./theme');
    expect(localStorage.getItem(STORAGE_KEY)).toBe('business');
    toggleTheme();
    expect(localStorage.getItem(STORAGE_KEY)).toBe('party');
    toggleTheme();
    expect(localStorage.getItem(STORAGE_KEY)).toBe('business');
  });
});
