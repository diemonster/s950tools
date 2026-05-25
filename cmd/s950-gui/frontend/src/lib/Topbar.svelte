<script lang="ts">
  import { onMount } from 'svelte';
  import { route, navigate, type Route } from './route';
  import { sync } from './sync';
  import {
    inputPorts, outputPorts, selectedIn, selectedOut, channel,
    phase, linkError,
    refreshPorts, refreshStatus, connect, disconnect,
  } from './state/connection';
  import { theme, toggleTheme } from './state/theme';

  // Page-specific slot count. `slotCount` is the loud part (e.g.
  // "3 / 100") and `slotNoun` is the descriptor ("programs", "samples")
  // that gets hidden on narrow viewports so the chip never clips.
  // `slotChip` is the legacy single-string prop kept for the few tabs
  // that haven't migrated yet; if set, it's rendered as-is.
  export let slotChip: string = '';
  export let slotCount: string = '';
  export let slotNoun: string = '';

  onMount(async () => {
    // Enumerate ports on mount + reflect any pre-existing connection
    // (the Wails backend keeps state across hot reloads in dev).
    await refreshPorts();
    await refreshStatus();
  });

  // The status chip merges link phase + per-tab edit state. Link
  // problems dominate (no point showing "Unsaved" when the device is
  // gone). When the device is fine, the edit-state dot takes over.
  $: statusChipLabel = (() => {
    if ($phase === 'connecting')   return 'Connecting…';
    if ($phase === 'error')        return 'Conn. error';
    if ($phase !== 'connected')    return 'Offline';
    return $sync.label;
  })();
  $: statusChipClass = (() => {
    if ($phase === 'connecting')   return 'chip chip--status chip--sending';
    if ($phase === 'error')        return 'chip chip--status chip--error';
    if ($phase !== 'connected')    return 'chip chip--status chip--error';
    if ($sync.state === 'synced')  return 'chip chip--status';
    return `chip chip--status chip--${$sync.state}`;
  })();

  function tabClass(name: Route): string {
    return name === $route ? 'tab active' : 'tab';
  }
</script>

<header class="topbar">
  <!-- The brand chip doubles as a theme toggle: clicking flips the
       `data-theme` attribute on <html> between 'business' (default
       light palette) and 'party' (dark palette + rainbow accents).
       Pure CSS swap of the --black/--white/--grey-* tokens; no
       per-component changes needed. -->
  <button
    type="button"
    class="logo logo--toggle"
    on:click={toggleTheme}
    title={$theme === 'party' ? 'Switch to Business Mode' : 'Switch to Party Mode'}
    aria-label="Toggle theme">s950-tools</button>
  <nav class="tabs">
    <button class={tabClass('program')}  on:click={() => navigate('program')}>Program</button>
    <button class={tabClass('keygroup')} on:click={() => navigate('keygroup')}>Keygroup</button>
    <button class={tabClass('sample')}   on:click={() => navigate('sample')}>Sample</button>
  </nav>
  <div class="spacer"></div>

  <!-- MIDI port pickers. Native <select> styled as a chip so it's
       accessible and keyboard-friendly without a custom dropdown. -->
  <label class="chip chip--select" title={$linkError || 'MIDI input port'}>
    <span class="chip__label">MIDI in</span>
    <select bind:value={$selectedIn}>
      <option value="">— pick —</option>
      {#each $inputPorts as p (p.name)}
        <option value={p.name}>{p.name}</option>
      {/each}
    </select>
  </label>
  <label class="chip chip--select" title={$linkError || 'MIDI output port'}>
    <span class="chip__label">MIDI out</span>
    <select bind:value={$selectedOut}>
      <option value="">— pick —</option>
      {#each $outputPorts as p (p.name)}
        <option value={p.name}>{p.name}</option>
      {/each}
    </select>
  </label>
  <label class="chip chip--select" title="MIDI channel (0..15)">
    <span class="chip__label">Ch</span>
    <select bind:value={$channel}>
      {#each Array(16) as _, i}
        <option value={i}>{i}</option>
      {/each}
    </select>
  </label>

  <!-- One button that flips role: Connect when disconnected, Disconnect
       when connected. Reduces topbar width vs two separate buttons. -->
  {#if $phase === 'connected'}
    <button type="button" class="chip chip--btn" on:click={disconnect}>Disconnect</button>
  {:else}
    <button
      type="button"
      class="chip chip--btn chip--btn-primary"
      disabled={$phase === 'connecting'}
      on:click={connect}>
      {$phase === 'connecting' ? 'Connecting…' : 'Connect'}
    </button>
  {/if}

  <span class={statusChipClass}>
    <span class="status-dot"></span>{statusChipLabel}
  </span>
  {#if slotCount}
    <span class="chip accent chip--slot">
      <strong>{slotCount}</strong>
      {#if slotNoun}<span class="chip__noun"> {slotNoun}</span>{/if}
    </span>
  {:else if slotChip}
    <span class="chip accent chip--slot">{slotChip}</span>
  {/if}
</header>

<style>
  /* Native <select> wrapped in a chip — keeps the topbar visually
     consistent but stays accessible (keyboard, screen reader). */
  :global(.chip--select) {
    cursor: pointer;
    /* Extra right padding leaves room for the custom ▾ chevron. */
    padding-right: 22px;
    position: relative;
  }
  :global(.chip--select select) {
    /* Strip the OS-native dropdown rendering (gradient + native
       chevron) so the chip looks like the rest of the topbar. */
    appearance: none;
    -webkit-appearance: none;
    -moz-appearance: none;
    background: transparent;
    border: none;
    font: inherit;
    color: inherit;
    cursor: pointer;
    outline: none;
    padding: 0 2px;
    margin: 0;
  }
  /* Custom chevron — matches the inline ▾ the static mockup uses on
     other chip-style triggers. */
  :global(.chip--select)::after {
    content: "▾";
    position: absolute;
    right: 10px;
    top: 50%;
    transform: translateY(-50%);
    pointer-events: none;
    color: var(--grey-dark);
    font-size: 10px;
  }

  /* Button-shaped chip variant for the Connect / Disconnect action.
     Same border + padding as .chip but with a hover state. */
  :global(.chip--btn) {
    cursor: pointer;
    font-weight: 500;
  }
  :global(.chip--btn:hover):not(:disabled) {
    background: var(--rb-yellow);
    color: var(--ink);
  }
  :global(.chip--btn-primary) {
    background: var(--rb-yellow);
    color: var(--ink);
  }
  :global(.chip--btn:disabled) {
    color: var(--grey-medium);
    cursor: not-allowed;
  }
</style>
