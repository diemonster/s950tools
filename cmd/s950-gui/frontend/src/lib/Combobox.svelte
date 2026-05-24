<script lang="ts">
  // Generic dropdown picker. Renders the same .field shell as a
  // numeric field so it slots into existing rows. Used for the
  // Soft/Loud "Sample" picker, and other "pick one of N" fields.
  //
  // Pattern:
  //   <Combobox
  //     value={kg.soft.sample}
  //     options={$samples.map((s) => ({ label: s.name, value: s.name, meta: padSlot(s.slot) }))}
  //     placeholder="(none)"
  //     on:change={(e) => selectedKeygroup.updateSoft({ sample: e.detail })} />

  import { createEventDispatcher } from 'svelte';

  export let value: string;
  export let options: Array<{ label: string; value: string; meta?: string }>;
  export let placeholder = '(none)';
  export let extra = '';
  export let allowNone = true;
  export let disabled = false;

  const dispatch = createEventDispatcher<{ change: string }>();

  let open = false;
  let rootEl: HTMLElement;

  function toggle() {
    if (disabled) return;
    open = !open;
  }
  function pick(v: string) {
    open = false;
    if (v !== value) dispatch('change', v);
  }
  function onWindowMouseDown(e: MouseEvent) {
    if (!open) return;
    if (rootEl && !rootEl.contains(e.target as Node)) open = false;
  }
  function onKeyDown(e: KeyboardEvent) {
    if (e.key === 'Escape') open = false;
  }

  // Selected label for display — fall back to placeholder if value
  // is empty.
  $: selectedLabel = (() => {
    const hit = options.find((o) => o.value === value);
    return hit?.label ?? value ?? '';
  })();
</script>

<svelte:window on:mousedown={onWindowMouseDown} on:keydown={onKeyDown} />

<div class="combobox" bind:this={rootEl}>
  <span
    class="field {extra}"
    class:is-disabled={disabled}
    class:combobox__field--open={open}
    on:click={toggle}
    role="button"
    tabindex="0">
    <span class="combobox__value">{selectedLabel || placeholder}</span>
    <span class="combobox__caret">▾</span>
  </span>
  {#if open}
    <ul class="combobox__menu">
      {#if allowNone}
        <li class:selected={value === ''} on:click={() => pick('')}>
          <span class="combobox__none">(none)</span>
        </li>
      {/if}
      {#each options as opt (opt.value)}
        <li class:selected={opt.value === value} on:click={() => pick(opt.value)}>
          <span>{opt.label}</span>
          {#if opt.meta}<span class="combobox__meta">{opt.meta}</span>{/if}
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  /* Fill the parent's value column so combobox lines up with sibling
     NumFields at the same width. */
  .combobox {
    position: relative;
    display: block;
    width: 100%;
  }
  .combobox > :global(.field) {
    width: 100%;
    box-sizing: border-box;
  }
  .combobox__field--open {
    border-radius: var(--r) var(--r) 0 0;
  }
  .combobox__value {
    flex: 1;
    text-align: left;
    -webkit-user-select: none;
    user-select: none;
  }
  .combobox__caret {
    color: var(--grey-dark);
    margin-left: 4px;
  }
  .combobox__menu {
    position: absolute;
    top: 100%;
    left: 0;
    right: 0;
    z-index: 50;
    margin: 0;
    padding: 4px;
    list-style: none;
    background: var(--white);
    border: var(--bw) solid var(--black);
    border-top: none;
    border-radius: 0 0 var(--r) var(--r);
    box-shadow: 0 6px 20px rgba(0, 0, 0, 0.2);
    max-height: 220px;
    overflow-y: auto;
    min-width: 160px;
  }
  .combobox__menu li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 4px 8px;
    font-family: var(--font-mono);
    font-size: 12px;
    cursor: pointer;
    border-radius: 3px;
  }
  .combobox__menu li:hover { background: var(--grey-light); }
  .combobox__menu li.selected { background: var(--rb-yellow); }
  .combobox__none {
    color: var(--grey-dark);
    font-style: italic;
  }
  .combobox__meta {
    font-size: 10px;
    color: var(--grey-dark);
  }
</style>
