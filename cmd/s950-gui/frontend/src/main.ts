// Theme attribute set before any rendering so a returning party-mode
// user doesn't see a flash of business-mode chrome on launch. Same
// key + values as lib/state/theme.ts (kept simple here to avoid
// pulling Svelte's store machinery into the pre-paint path).
{
  const t = localStorage.getItem('s950-tools.theme');
  document.documentElement.dataset.theme = t === 'party' ? 'party' : 'business';
}

// Suppress the browser-level context menu (which exposes the dev
// inspector in `wails dev` builds). Wails already disables it in
// production via `EnableDefaultContextMenu: false`, but in dev the
// "Inspect" entry shows through and looks unprofessional during
// demos. Exempt text inputs so users can still right-click them for
// the OS-native copy / paste menu.
window.addEventListener('contextmenu', (e) => {
  const t = e.target as HTMLElement | null;
  if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return;
  e.preventDefault();
});

// Global stylesheet — palette, shell, form controls, modal. Page-
// specific styles live in each .svelte component's <style> block.
import './shared.css';

import App from './App.svelte';

const app = new App({
  target: document.getElementById('app')!,
});

// Dev-mock harness — when the page is opened with `?mock=1` (which
// the headless-Chrome screenshot driver always sets), drive the
// catalog + sample-load sequence that connect() normally triggers, so
// views render with realistic populated content. No effect in
// production (Wails strips query string).
if (new URLSearchParams(location.search).get('mock') === '1') {
  Promise.all([
    import('./lib/state/catalog'),
  ]).then(([catalog]) => {
    setTimeout(async () => {
      await catalog.refreshCatalog();
      await catalog.ensureProgramLoaded(0, true);
      await catalog.ensureSampleLoaded(0, true);
    }, 80);
  });
}

export default app;
