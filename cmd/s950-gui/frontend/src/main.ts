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

export default app;
