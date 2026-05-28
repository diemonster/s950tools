<script lang="ts">
  // SysEx wire-log overlay. Subscribes to the backend's "wire:traffic"
  // events (one per inbound/outbound envelope) and renders a scrolling
  // hex dump. Hidden by default; the toggle lives in the statusbar so
  // it never floats over page content.
  //
  // Buffer is capped at MAX_MESSAGES so a long sample-dump session
  // doesn't blow up the WebView heap — the oldest entries roll off
  // FIFO and the panel says "(N older messages dropped)".

  import { onMount, onDestroy } from 'svelte';
  import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime';

  type Wire = {
    direction: 'tx' | 'rx';
    length: number;
    hexBytes: string;
    stampMs: number;
  };

  const MAX_MESSAGES = 500;

  let open = false;
  let messages: Wire[] = [];
  let dropped = 0;
  let bodyEl: HTMLDivElement | undefined;
  let autoScroll = true;

  onMount(() => {
    EventsOn('wire:traffic', (m: Wire) => {
      messages = messages.length >= MAX_MESSAGES
        ? (dropped++, messages.slice(1).concat(m))
        : messages.concat(m);
      // Defer scroll to next tick so the new row exists in the DOM.
      if (autoScroll && open) {
        queueMicrotask(() => {
          if (bodyEl) bodyEl.scrollTop = bodyEl.scrollHeight;
        });
      }
    });
  });

  onDestroy(() => EventsOff('wire:traffic'));

  function clear() {
    messages = [];
    dropped = 0;
  }

  // HH:MM:SS.mmm — short enough for a left-column timestamp, long
  // enough to correlate with anything else the user is timing against.
  function fmtTime(ms: number): string {
    const d = new Date(ms);
    const pad = (n: number, w = 2) => String(n).padStart(w, '0');
    return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
  }

  // Detect when the user scrolls away from the bottom — if they're
  // reading an old message, we don't want auto-scroll to whisk them
  // back to the live tail on every new event.
  function onScroll() {
    if (!bodyEl) return;
    const near = bodyEl.scrollHeight - bodyEl.scrollTop - bodyEl.clientHeight < 20;
    autoScroll = near;
  }
</script>

<button
  type="button"
  class="wire-toggle"
  class:wire-toggle--open={open}
  title={open ? 'Hide wire log' : 'Show SysEx wire log'}
  on:click={() => (open = !open)}>
  ⌥ {messages.length}{dropped > 0 ? '+' : ''}
</button>

{#if open}
  <aside class="wire-log" role="log" aria-label="SysEx wire log">
    <header class="wire-log__head">
      <span class="wire-log__title">SysEx wire log</span>
      <span class="wire-log__count">{messages.length}{dropped > 0 ? ` (+${dropped} dropped)` : ''}</span>
      <button type="button" class="wire-log__btn" on:click={clear}>Clear</button>
      <button type="button" class="wire-log__btn" on:click={() => (open = false)}>×</button>
    </header>
    <div class="wire-log__body" bind:this={bodyEl} on:scroll={onScroll}>
      {#each messages as m, i (i + ':' + m.stampMs)}
        <div class="wire-log__row wire-log__row--{m.direction}">
          <span class="wire-log__ts">{fmtTime(m.stampMs)}</span>
          <span class="wire-log__dir">{m.direction.toUpperCase()}</span>
          <span class="wire-log__size">{m.length}B</span>
          <span class="wire-log__hex">{m.hexBytes}</span>
        </div>
      {/each}
      {#if messages.length === 0}
        <div class="wire-log__empty">
          No traffic yet. Connect + interact with the S950 to populate.
        </div>
      {/if}
    </div>
  </aside>
{/if}

<style>
  /* Inline toggle — rendered inside the statusbar so it never floats
     over page content. Counter shows current buffered messages; "+"
     suffix indicates the oldest have rolled off the MAX_MESSAGES cap.
     position:relative + z-index lifts it above the .modal-backdrop
     (z=100, see shared.css), so when the user is stuck on a transfer
     modal they can still pop the wire log to diagnose. The drawer
     uses an even higher z so it stacks over the modal when open. */
  .wire-toggle {
    position: relative;
    z-index: 101;
    font-family: var(--font-mono);
    font-size: 10px;
    padding: 1px 7px;
    border: 1px solid var(--grey-medium);
    border-radius: 3px;
    background: transparent;
    color: inherit;
    cursor: pointer;
    text-transform: none;
    letter-spacing: 0;
  }
  .wire-toggle:hover { background: var(--grey-light); }
  .wire-toggle--open {
    background: var(--ink);
    color: var(--rb-yellow);
    border-color: var(--black);
  }

  /* Drawer pinned to the bottom-right; intentionally non-modal so the
     user can drive the app while watching wire traffic in real time.
     z=110 keeps it above the modal-backdrop (z=100) — diagnosing a
     stuck SysEx exchange while the modal is up requires the drawer
     to render on top. */
  .wire-log {
    position: fixed;
    right: 12px;
    bottom: 36px;
    width: min(720px, calc(100vw - 24px));
    height: 360px;
    z-index: 110;
    background: var(--canvas);
    color: var(--canvas-text);
    border: 1px solid var(--black);
    border-radius: var(--r);
    box-shadow: 0 8px 32px rgba(0,0,0,0.35);
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .wire-log__head {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    background: var(--ink);
    color: var(--paper);
    border-bottom: 1px solid var(--black);
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .wire-log__title {
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.08em;
  }
  .wire-log__count {
    color: var(--grey-medium);
    margin-right: auto;
  }
  .wire-log__btn {
    border: 1px solid var(--grey-medium);
    background: transparent;
    color: var(--paper);
    font: inherit;
    padding: 1px 7px;
    border-radius: 3px;
    cursor: pointer;
  }
  .wire-log__btn:hover { background: rgba(255,255,255,0.1); }

  .wire-log__body {
    flex: 1 1 auto;
    overflow-y: auto;
    overflow-x: hidden;
    padding: 4px 0;
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .wire-log__row {
    display: grid;
    /* timestamp · direction · length · hex (wraps) */
    grid-template-columns: 96px 28px 50px 1fr;
    gap: 6px;
    padding: 1px 10px;
    line-height: 1.35;
  }
  .wire-log__row:hover { background: rgba(255,255,255,0.03); }
  .wire-log__ts   { color: var(--grey-medium); }
  .wire-log__dir  { font-weight: 700; }
  .wire-log__size { color: var(--grey-medium); text-align: right; }
  .wire-log__hex  {
    word-break: break-all;
    color: var(--canvas-text);
  }
  .wire-log__row--tx .wire-log__dir { color: var(--rb-yellow); }
  .wire-log__row--rx .wire-log__dir { color: var(--rb-cyan); }
  .wire-log__empty {
    padding: 24px;
    text-align: center;
    color: var(--grey-medium);
    font-style: italic;
  }
</style>
