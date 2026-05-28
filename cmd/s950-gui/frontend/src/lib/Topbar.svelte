<script lang="ts">
  import { onMount } from 'svelte';
  import { route, navigate, type Route } from './route';
  import { sync } from './sync';
  import {
    inputPorts, outputPorts, serialPortsList,
    selectedIn, selectedOut, channel,
    transportKind, baud, SERIAL_BAUDS,
    phase, linkError,
    refreshPorts, refreshStatus, connect, disconnect,
    pickSerial, pickMidi,
  } from './state/connection';
  import { memoryUsage, totalWords, expansionEnabled } from './state/memory';
  import { theme, toggleTheme } from './state/theme';

  // Picker option values are prefixed so a single <select> can hold
  // both MIDI and serial entries while still letting the change
  // handler tell them apart. "midi:<name>" vs "serial:<name>".
  const MIDI = 'midi:';
  const SERIAL = 'serial:';

  function pickerValue(kind: 'midi' | 'serial', name: string): string {
    return name ? `${kind === 'serial' ? SERIAL : MIDI}${name}` : '';
  }
  function currentInValue(): string {
    return $transportKind === 'serial' ? pickerValue('serial', $selectedIn) : pickerValue('midi', $selectedIn);
  }
  function currentOutValue(): string {
    return $transportKind === 'serial' ? pickerValue('serial', $selectedOut) : pickerValue('midi', $selectedOut);
  }

  function onPickIn(e: Event) {
    const raw = (e.currentTarget as HTMLSelectElement).value;
    if (raw.startsWith(SERIAL)) pickSerial(raw.slice(SERIAL.length));
    else pickMidi('in', raw.startsWith(MIDI) ? raw.slice(MIDI.length) : '');
  }
  function onPickOut(e: Event) {
    const raw = (e.currentTarget as HTMLSelectElement).value;
    if (raw.startsWith(SERIAL)) pickSerial(raw.slice(SERIAL.length));
    else pickMidi('out', raw.startsWith(MIDI) ? raw.slice(MIDI.length) : '');
  }

  // Short K/M number formatter for the memory chip. 412000 → "412K",
  // 1536000 → "1.5M". Keeps the chip readable at narrow widths.
  function fmtWords(n: number): string {
    if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
    if (n >= 1_000)     return `${Math.round(n / 1_000)}K`;
    return String(n);
  }
  $: memPct = $totalWords > 0 ? Math.round(($memoryUsage.usedWords / $totalWords) * 100) : 0;

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
    if ($phase === 'connecting')   return 'Connecting...';
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

  <!-- Port pickers. Two chips that each list MIDI ports + RS-232
       serial devices as alternatives. The native <select> is
       positioned absolute + opacity 0 over the chip so any pixel
       opens the dropdown; .chip__value renders the current pick
       as plain text behind it. Picking a serial entry in either
       chip auto-mirrors to the other (RS-232 is bidirectional on
       a single cable), and the channel chip flips to a baud chip
       so the user doesn't have to pretend the MIDI channel
       matters on a point-to-point serial link. -->
  <label class="chip chip--select" title={$linkError || ($transportKind === 'serial' ? 'RS-232 port (bidirectional)' : 'MIDI input port')}>
    <span class="chip__label">{$transportKind === 'serial' ? 'RS-232' : 'MIDI in'}</span>
    <span class="chip__value">{$selectedIn || '— pick —'}</span>
    <select value={currentInValue()} on:change={onPickIn}>
      <option value="">— pick —</option>
      {#if $inputPorts.length > 0}
        <optgroup label="MIDI inputs">
          {#each $inputPorts as p (p.name)}
            <option value={pickerValue('midi', p.name)}>{p.name}</option>
          {/each}
        </optgroup>
      {/if}
      {#if $serialPortsList.length > 0}
        <optgroup label="RS-232 (serial)">
          {#each $serialPortsList as p (p.name)}
            <option value={pickerValue('serial', p.name)}>{p.name}</option>
          {/each}
        </optgroup>
      {/if}
    </select>
  </label>
  {#if $transportKind !== 'serial'}
    <label class="chip chip--select" title={$linkError || 'MIDI output port'}>
      <span class="chip__label">MIDI out</span>
      <span class="chip__value">{$selectedOut || '— pick —'}</span>
      <select value={currentOutValue()} on:change={onPickOut}>
        <option value="">— pick —</option>
        {#if $outputPorts.length > 0}
          <optgroup label="MIDI outputs">
            {#each $outputPorts as p (p.name)}
              <option value={pickerValue('midi', p.name)}>{p.name}</option>
            {/each}
          </optgroup>
        {/if}
        {#if $serialPortsList.length > 0}
          <optgroup label="RS-232 (serial)">
            {#each $serialPortsList as p (p.name)}
              <option value={pickerValue('serial', p.name)}>{p.name}</option>
            {/each}
          </optgroup>
        {/if}
      </select>
    </label>
  {/if}
  {#if $transportKind === 'serial'}
    <label class="chip chip--select" title="RS-232 baud rate (must match S950's overall-settings page)">
      <span class="chip__label">Baud</span>
      <span class="chip__value">{$baud}</span>
      <select bind:value={$baud}>
        {#each SERIAL_BAUDS as b}
          <option value={b}>{b}</option>
        {/each}
      </select>
    </label>
  {:else}
    <label class="chip chip--select" title="MIDI channel (0..15)">
      <span class="chip__label">Ch</span>
      <span class="chip__value">{$channel}</span>
      <select bind:value={$channel}>
        {#each Array(16) as _, i}
          <option value={i}>{i}</option>
        {/each}
      </select>
    </label>
  {/if}

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
      {$phase === 'connecting' ? 'Connecting...' : 'Connect'}
    </button>
  {/if}

  <span class={statusChipClass}>
    <span class="status-dot"></span>{statusChipLabel}
  </span>
  {#if $phase === 'connected'}
    <!-- Memory chip — visible only while connected; auto-populated
         by scanMemory() which runs after Connect / Get / Apply. The
         S950 has no SysEx surface for free memory, so the value
         comes from summing TotalWords across every SPRM. -->
    <span class="chip chip--mem" title={$memoryUsage.scanning ? 'Scanning device memory…' : `${memPct}% used · scan re-runs after Get / Apply`}>
      {#if $memoryUsage.scanning && $memoryUsage.scannedAt === null}
        <strong>Scanning…</strong>
      {:else}
        <strong>{fmtWords($memoryUsage.usedWords)}</strong>
        <span class="chip__noun"> / {fmtWords($totalWords)} words</span>
      {/if}
    </span>
    <!-- EXM005 expansion toggle. S950 has no way to query the
         expansion presence over SysEx, so the user flips this once
         and it persists via localStorage. When on, the memory chip
         uses the 1.57M-word ceiling; when off, the 512K base. -->
    <button
      type="button"
      class="chip chip--exm"
      class:chip--exm-on={$expansionEnabled}
      title={$expansionEnabled
        ? 'EXM005 memory expansion ON · click to switch to base S950 (512K words)'
        : 'Base S950 (512K words) · click if you have the EXM005 expansion (2.25 MB)'}
      on:click={() => expansionEnabled.update((v) => !v)}>
      EXM{$expansionEnabled ? ' ✓' : ''}
    </button>
  {/if}
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
     consistent but stays accessible (keyboard, screen reader).
     Layout is inline-flex so the label + value spans sit on a row
     and the absolutely-positioned select can size to the full chip
     box, capturing clicks anywhere inside (not just on its
     intrinsic text width). */
  :global(.chip--select) {
    cursor: pointer;
    /* Extra right padding leaves room for the custom ▾ chevron. */
    padding-right: 22px;
    position: relative;
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  :global(.chip--select .chip__value) {
    /* Visible rendering of the current selection. Clicks must fall
       through to the transparent select underneath, so this span is
       inert. */
    pointer-events: none;
  }
  :global(.chip--select select) {
    /* Strip the OS-native dropdown rendering and stretch the
       select over the entire chip with opacity:0, so every pixel
       inside the chip border opens the picker on click. */
    -webkit-appearance: none;
    -moz-appearance: none;
    appearance: none;
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    opacity: 0;
    cursor: pointer;
    border: none;
    outline: none;
    font: inherit;
    color: inherit;
    margin: 0;
    padding: 0;
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
