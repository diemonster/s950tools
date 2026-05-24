// Global stylesheet — palette, shell, form controls, modal. Page-
// specific styles live in each .svelte component's <style> block.
import './shared.css';

import App from './App.svelte';

const app = new App({
  target: document.getElementById('app')!,
});

export default app;
