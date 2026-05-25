// Theme attribute set before any rendering so a returning party-mode
// user doesn't see a flash of business-mode chrome on launch. Same
// key + values as lib/state/theme.ts (kept simple here to avoid
// pulling Svelte's store machinery into the pre-paint path).
{
  const t = localStorage.getItem('s950-tools.theme');
  document.documentElement.dataset.theme = t === 'party' ? 'party' : 'business';
}

// Global stylesheet — palette, shell, form controls, modal. Page-
// specific styles live in each .svelte component's <style> block.
import './shared.css';

import App from './App.svelte';

const app = new App({
  target: document.getElementById('app')!,
});

export default app;
