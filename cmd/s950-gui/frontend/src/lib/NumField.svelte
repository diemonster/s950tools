<script lang="ts">
  // Numeric value editor with four input affordances:
  //   1. Step buttons (−/+) on either side
  //   2. Mouse wheel (shift = bigger step)
  //   3. Click-and-drag vertically (shift = bigger step)
  //   4. Click the value to type a number directly (enter/blur commits,
  //      escape cancels)
  // Renders the same .field shell as the mockups, so it slots into
  // existing layouts. Emits `change` with the new (clamped) value.
  //
  // Usage:
  //   <NumField
  //     value={kg.lowKey}
  //     min={0} max={127} step={1}
  //     format={(v) => `${noteName(v)} (${v})`}
  //     on:change={(e) => selectedKeygroup.update({ lowKey: e.detail })} />

  import { createEventDispatcher, tick } from 'svelte';

  export let value: number;
  export let min = -Infinity;
  export let max = Infinity;
  export let step = 1;
  // Optional display formatter. Default: plain string.
  export let format: (v: number) => string = (v) => String(v);
  // Multiplier for shift+wheel / shift+drag. 10 by default so big
  // ranges (vel 0..127) are tractable.
  export let shiftMul = 10;
  // Pixels-per-step for click-and-drag. Lower = more sensitive.
  export let dragPx = 4;
  // Extra classes to apply to the field shell (e.g. "field--xs",
  // "field--yellow"). The base "field" class is always present.
  export let extra = '';
  // Disables all interactions and dims the field via .is-disabled.
  export let disabled = false;

  const dispatch = createEventDispatcher<{ change: number }>();

  let editing = false;
  let inputEl: HTMLInputElement | undefined;
  let inputText = '';

  // Drag state. dragActive means a mousedown happened on the value;
  // dragMoved flips true once the cursor crosses the threshold,
  // converting the gesture from "click-to-edit" into "drag-to-change".
  let dragActive = false;
  let dragMoved = false;
  let dragStartY = 0;
  let dragStartValue = 0;
  let dragMul = 1;

  const clamp = (v: number) => Math.min(max, Math.max(min, v));

  function emit(v: number) {
    const next = clamp(v);
    if (next !== value) dispatch('change', next);
  }

  function dec(mul = 1) { emit(value - step * mul); }
  function inc(mul = 1) { emit(value + step * mul); }

  // ---------- Wheel ----------
  function onWheel(e: WheelEvent) {
    if (editing || disabled) return;
    e.preventDefault();
    const mul = e.shiftKey ? shiftMul : 1;
    if (e.deltaY < 0) inc(mul);
    else              dec(mul);
  }

  // ---------- Drag ↔ click-to-type ----------
  function onValueMouseDown(e: MouseEvent) {
    if (editing || disabled) return;
    if (e.button !== 0) return;
    dragActive = true;
    dragMoved = false;
    dragStartY = e.clientY;
    dragStartValue = value;
    dragMul = e.shiftKey ? shiftMul : 1;
    e.preventDefault(); // suppresses text selection
  }
  function onWindowMouseMove(e: MouseEvent) {
    if (!dragActive) return;
    const dy = dragStartY - e.clientY; // up = positive
    if (!dragMoved && Math.abs(dy) < 3) return; // dead zone
    dragMoved = true;
    const delta = Math.round(dy / dragPx) * step * dragMul;
    emit(dragStartValue + delta);
  }
  async function onWindowMouseUp() {
    if (!dragActive) return;
    const wasDrag = dragMoved;
    dragActive = false;
    dragMoved = false;
    if (!wasDrag) {
      // Treated as a click → enter type-in mode.
      await startEdit();
    }
  }

  async function startEdit() {
    if (editing || disabled) return;
    inputText = String(value);
    editing = true;
    await tick();
    inputEl?.focus();
    inputEl?.select();
  }

  function commitEdit() {
    if (!editing) return;
    editing = false;
    const v = parseFloat(inputText);
    if (!Number.isNaN(v)) emit(v);
  }
  function cancelEdit() {
    editing = false;
  }
  function onInputKey(e: KeyboardEvent) {
    if (e.key === 'Enter')      commitEdit();
    else if (e.key === 'Escape') cancelEdit();
  }
</script>

<svelte:window on:mousemove={onWindowMouseMove} on:mouseup={onWindowMouseUp} />

<span
  class="field numfield-shell {extra}"
  class:is-disabled={disabled}
  on:wheel|preventDefault|nonpassive={onWheel}>
  <span class="stepper" on:click={() => dec()} role="button" tabindex="-1">−</span>
  {#if editing}
    <input
      class="numfield__input"
      bind:this={inputEl}
      bind:value={inputText}
      on:blur={commitEdit}
      on:keydown={onInputKey}
      type="text"
      inputmode="decimal" />
  {:else}
    <span
      class="numfield__value"
      on:mousedown={onValueMouseDown}
      title="scroll · drag · click to type">
      {format(value)}
    </span>
  {/if}
  <span class="stepper" on:click={() => inc()} role="button" tabindex="-1">+</span>
</span>

<style>
  /* Fill the row's value column so every NumField is the same width
     within a column — keeps "0" and "+0.00 st" visually aligned. */
  .numfield-shell {
    width: 100%;
    box-sizing: border-box;
  }
  .numfield__value {
    flex: 1;
    text-align: center;
    cursor: ns-resize;
    -webkit-user-select: none;
    user-select: none;
    /* min-width keeps the cell from contracting when going from "99"
       to "0" — prevents layout shift on ADSR strips. */
    padding: 0 4px;
    min-width: 18px;
    /* Multi-word formatted values (e.g. "+0.00 st") must stay on
       one line — otherwise the cell wraps to two rows on narrow
       columns. */
    white-space: nowrap;
    overflow: hidden;
    text-overflow: clip;
  }
  .numfield__input {
    flex: 1;
    border: none;
    background: transparent;
    font: inherit;
    color: inherit;
    text-align: center;
    outline: none;
    padding: 0 4px;
    width: 0; /* lets flex shrink it correctly */
    min-width: 0;
  }

  /* Compact override for narrow contexts (ADSR strip, LFO panel).
     Drops every interior padding so a stepper+value+stepper fits
     inside a ~50px cell. */
  :global(.field--xs) .numfield__value {
    min-width: 14px;
    padding: 0 2px;
  }
  :global(.field--xs) :global(.stepper) {
    padding: 0 2px;
  }
</style>
