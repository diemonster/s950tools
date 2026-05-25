// Two-mode UI theme. 'business' is the daytime palette (white panels,
// black borders — what the app shipped with); 'party' inverts to a
// dark canvas with the rainbow accents popping off. Driven by a CSS
// `data-theme` attribute on <html>; the actual color swap lives in
// shared.css as a [data-theme="party"] override block, so adding a
// new themable token is a one-line CSS addition without touching JS.
//
// State persists across sessions via localStorage — when the user
// reopens the app, the last-picked theme sticks. main.ts also reads
// the same key pre-paint so launches never flash the wrong palette.

import { writable } from 'svelte/store';

export type Theme = 'business' | 'party';

const STORAGE_KEY = 's950-tools.theme';

function readInitial(): Theme {
  if (typeof localStorage === 'undefined') return 'business';
  const stored = localStorage.getItem(STORAGE_KEY);
  return stored === 'party' ? 'party' : 'business';
}

export const theme = writable<Theme>(readInitial());

// Side-effect subscriber: whenever the store changes, mirror it to
// the DOM attribute + localStorage. Wails apps survive process
// restarts on the same machine, so localStorage is durable enough
// for a UI preference like this.
theme.subscribe((t) => {
  if (typeof document !== 'undefined') {
    document.documentElement.dataset.theme = t;
  }
  if (typeof localStorage !== 'undefined') {
    localStorage.setItem(STORAGE_KEY, t);
  }
});

export function toggleTheme() {
  theme.update((t) => (t === 'party' ? 'business' : 'party'));
}
