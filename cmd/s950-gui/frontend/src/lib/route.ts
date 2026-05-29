// Simple hash-free routing — Wails apps have no URL bar, so we just
// track which tab is active in a writable store. Cross-tab "Edit ↗"
// jumps set the selected entity in its own store, then flip the route.

import { writable } from 'svelte/store';

export type Route = 'program' | 'keygroup' | 'sample';

// Initial route honours `?route=keygroup|sample|program` when present,
// so the dev screenshot driver can deep-link into a specific view
// from headless Chrome. No-op in production (Wails strips query).
function initialRoute(): Route {
  if (typeof window === 'undefined') return 'program';
  const q = new URLSearchParams(window.location.search).get('route');
  return q === 'keygroup' || q === 'sample' ? q : 'program';
}

export const route = writable<Route>(initialRoute());

export function navigate(to: Route) {
  route.set(to);
}
