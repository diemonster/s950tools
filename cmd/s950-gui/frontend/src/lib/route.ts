// Simple hash-free routing — Wails apps have no URL bar, so we just
// track which tab is active in a writable store. Cross-tab "Edit ↗"
// jumps set the selected entity in its own store, then flip the route.

import { writable } from 'svelte/store';

export type Route = 'program' | 'keygroup' | 'sample';

export const route = writable<Route>('program');

export function navigate(to: Route) {
  route.set(to);
}
