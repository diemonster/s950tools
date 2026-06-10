<script lang="ts">
  import { onMount } from 'svelte';
  import { route, navigate, type Route } from './route';
  import { sync } from './sync';
  import {
    inputPorts, outputPorts, serialPortsList,
    selectedIn, selectedOut, channel,
    transportKind, baud, SERIAL_BAUDS, status,
    phase, linkError, serialUnresponsive, dismissSerialUnresponsive,
    refreshPorts, refreshStatus, connect, disconnect,
    pickSerial, pickMidi,
  } from './state/connection';
  import { memoryUsage, totalWords, expansionEnabled } from './state/memory';
  import {
    refreshState, refreshAllFromDevice, cancelRefresh, resetRefresh,
  } from './state/refresh';
  import { theme, toggleTheme } from './state/theme';
  import {
    midiThru, midiThruError, startThru, stopThru, initMidiThruEvents,
    syncThruStatus, setThruPref, thruCaps, loadThruCaps,
  } from './state/midithru';
  import * as App from '../../wailsjs/go/main/App';

  // Picker option values are prefixed so a single <select> can hold
  // both MIDI and serial entries while still letting the change
  // handler tell them apart. "midi:<name>" vs "serial:<name>".
  // The PROBE sentinel is a third synthetic option that triggers
  // the auto-discovery flow instead of selecting a port directly.
  const MIDI = 'midi:';
  const SERIAL = 'serial:';
  const PROBE = '__probe__';

  // Probe state machine — drives the dropdown entry's label so the
  // user sees "Probing…" while scanning. On success the picker
  // auto-selects the discovered port + flips to serial; on miss it
  // surfaces the error inline (via linkError, same path as Connect).
  let probing = false;
  async function runProbe() {
    if (probing) return;
    probing = true;
    linkError.set('');
    try {
      const result = await App.ProbeForS950();
      pickSerial(result.port);
      // SERIAL_BAUDS is the closed list our picker offers; the
      // probe's baud might be a sub-bound (9600/19200) that's in
      // the list anyway, so set directly.
      baud.set(result.baud as any);
    } catch (e: any) {
      linkError.set(String(e?.message ?? e));
    } finally {
      probing = false;
    }
  }

  function pickerValue(kind: 'midi' | 'serial', name: string): string {
    return name ? `${kind === 'serial' ? SERIAL : MIDI}${name}` : '';
  }
  function currentInValue(): string {
    return $transportKind === 'serial' ? pickerValue('serial', $selectedIn) : pickerValue('midi', $selectedIn);
  }
  function currentOutValue(): string {
    return $transportKind === 'serial' ? pickerValue('serial', $selectedOut) : pickerValue('midi', $selectedOut);
  }

  // ---------- MIDI thru ----------
  // Select-option encoding mirrors the port picker: a sentinel for
  // the virtual port, a prefix for physical inputs, '' for off.
  const THRU_VIRTUAL = '__virtual__';
  const THRU_PORT = 'port:';

  $: thruSelectValue = !$midiThru.active
    ? ''
    : $midiThru.virtual ? THRU_VIRTUAL : THRU_PORT + $midiThru.source;
  // Chip-width-friendly source label: the virtual port's full name
  // is long; physical names pass through (ellipsised by CSS).
  $: thruSourceShort = $midiThru.virtual ? 'virtual' : $midiThru.source;

  // thruBusy serialises chip interactions: two quick picks would
  // otherwise interleave their stop/start round-trips (same hazard
  // the `probing` flag guards in runProbe). The select is disabled
  // while a round-trip is in flight.
  let thruBusy = false;
  async function onThruChange(e: Event) {
    const sel = e.currentTarget as HTMLSelectElement;
    const raw = sel.value;
    // Snap back visually; the reactive thruSelectValue re-asserts
    // the real state once the binding round-trip lands (mirrors the
    // probe option's pattern).
    sel.value = thruSelectValue;
    if (thruBusy) return;
    thruBusy = true;
    try {
      // Persist the choice so the next serial connect restores it
      // (autoStartThru in connection.ts; 'virtual' is the default).
      setThruPref(raw === THRU_VIRTUAL ? 'virtual' : raw === '' ? '' : raw);
      // Always stop first, regardless of the store's view — after a
      // frontend reload the store can lag the backend, and Stop on
      // an idle backend is a harmless no-op. This is what makes the
      // chip self-correcting instead of dead-ending on the
      // backend's "already running" error.
      await stopThru();
      if (raw === THRU_VIRTUAL) {
        await startThru('', true);
      } else if (raw.startsWith(THRU_PORT)) {
        await startThru(raw.slice(THRU_PORT.length), false);
      }
    } finally {
      thruBusy = false;
    }
  }

  function onPickIn(e: Event) {
    const sel = e.currentTarget as HTMLSelectElement;
    const raw = sel.value;
    if (raw === PROBE) {
      // Snap the select back to the previous value visually — the
      // probe option is a one-shot trigger, not a selection. Without
      // this the dropdown would stay stuck on "Probe…" until the
      // user picks something else.
      sel.value = currentInValue();
      void runProbe();
      return;
    }
    if (raw.startsWith(SERIAL)) pickSerial(raw.slice(SERIAL.length));
    else pickMidi('in', raw.startsWith(MIDI) ? raw.slice(MIDI.length) : '');
  }
  function onPickOut(e: Event) {
    const sel = e.currentTarget as HTMLSelectElement;
    const raw = sel.value;
    if (raw === PROBE) {
      sel.value = currentOutValue();
      void runProbe();
      return;
    }
    if (raw.startsWith(SERIAL)) pickSerial(raw.slice(SERIAL.length));
    else pickMidi('out', raw.startsWith(MIDI) ? raw.slice(MIDI.length) : '');
  }

  // Refresh modal is gated on transport === 'serial' (the chip is
  // hidden on MIDI for the same reason — full-state pull is
  // multi-minute on MIDI's line rate and we don't want users to
  // discover that the hard way). The modal stays open through the
  // 'done' / 'error' terminals so the user can read the outcome
  // before dismissing. Cancel is best-effort: the in-flight Wails
  // call completes before the loop checks the flag.
  let refreshModalOpen = false;
  // Audio pass toggle on the idle-phase confirmation. Default ON
  // (matches the documented behavior — refresh is supposed to
  // include audio). Users can uncheck for a fast metadata-only
  // run when they know audio hasn't drifted (e.g. they only
  // edited programs on the front panel).
  let refreshIncludeAudio = true;
  function openRefreshModal() {
    refreshModalOpen = true;
    refreshIncludeAudio = true; // reset toggle each open
  }
  function closeRefreshModal() {
    refreshModalOpen = false;
    resetRefresh();
  }
  async function startRefresh() {
    await refreshAllFromDevice(refreshIncludeAudio);
    // Modal stays open on 'done' / 'error' so the user reads the
    // outcome — closeRefreshModal() resets the store on dismiss.
  }
  $: refreshRunning = $refreshState.phase === 'catalog'
                   || $refreshState.phase === 'samples'
                   || $refreshState.phase === 'programs'
                   || $refreshState.phase === 'audio';

  // ---------- System (Overall) settings modal ----------
  // Backed by App.GetOverall / App.SetOverall — the 80-byte OVS block
  // (basic channel, omni, transmit channel, pitch-wheel range, etc).
  // Three fields are deliberately NOT exposed:
  //   - ControllerSelect (MIDI vs RS-232C) is silently dropped by
  //     firmware — only the front-panel MIDI menu can change it.
  //   - BaudRate similarly belongs on the front panel; the topbar's
  //     Baud chip already negotiates the HOST side.
  //   - MPEN (OVS byte 60) — purpose unconfirmed; the dxzl mnemonic
  //     was originally mis-expanded to "MPE" (MIDI Polyphonic
  //     Expression, a 2018 spec that postdates the S950 by 30 years).
  //     Don't toggle a byte whose semantics nobody has verified.
  // All three round-trip via OverallSettings.Raw (the backend's
  // SetOverall encode-from-Raw + overwrite-modelled-fields pattern
  // preserves bytes we don't touch).
  let overallModalOpen = false;
  let overallPhase: 'idle' | 'loading' | 'editing' | 'saving' | 'done' | 'error' = 'idle';
  let overallError = '';
  // The currently-edited OverallSettings, with Raw preserved from the
  // initial fetch. null until the first successful GetOverall.
  let overallEdit: any = null;
  async function openOverallModal() {
    overallModalOpen = true;
    overallPhase = 'loading';
    overallError = '';
    cachePhase = 'idle';
    cacheError = '';
    try {
      overallEdit = await App.GetOverall();
      overallPhase = 'editing';
    } catch (e: any) {
      overallPhase = 'error';
      overallError = String(e?.message ?? e);
    }
  }
  function closeOverallModal() {
    overallModalOpen = false;
    overallPhase = 'idle';
    overallEdit = null;
    overallError = '';
    cachePhase = 'idle';
    cacheError = '';
  }

  // ---------- Host waveform-cache maintenance ----------
  // Clears the Phase 1B disk cache (~/Library/Caches/s950-tools on
  // macOS). Host-disk-only: nothing is sent to the device, and the
  // live session's in-memory audio stays attached — the only effect
  // is that the NEXT session re-pulls audio from the sampler instead
  // of hydrating from disk. Two-step confirm guards the click; the
  // action lives in the System modal but deliberately does not
  // depend on the OVS fetch succeeding, so it stays usable when the
  // sampler is disconnected (that's exactly when you may want to
  // clean up a stale cache).
  let cachePhase: 'idle' | 'confirm' | 'clearing' | 'done' | 'error' = 'idle';
  let cacheError = '';
  async function clearWaveCache() {
    cachePhase = 'clearing';
    cacheError = '';
    try {
      await App.ClearWaveformCache();
      cachePhase = 'done';
    } catch (e: any) {
      cachePhase = 'error';
      cacheError = String(e?.message ?? e);
    }
  }
  async function saveOverall() {
    if (!overallEdit) return;
    overallPhase = 'saving';
    overallError = '';
    try {
      await App.SetOverall(overallEdit);
      overallPhase = 'done';
    } catch (e: any) {
      overallPhase = 'error';
      overallError = String(e?.message ?? e);
    }
  }
  // ProgName is a 10-char ASCII field on the wire (space-padded);
  // extracted here so the markup stays template-clean — Svelte 3
  // can't always parse a TS cast + assignment inside an attribute.
  function onProgNameInput(e: Event) {
    if (!overallEdit) return;
    const raw = (e.target as HTMLInputElement).value;
    overallEdit.ProgName = raw.padEnd(10).slice(0, 10);
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
    // MIDI-thru state pushes (counter updates while forwarding).
    // Wired here rather than at module load so the store module is
    // importable in tests without a Wails runtime.
    try {
      const rt = await import('../../wailsjs/runtime/runtime');
      initMidiThruEvents(rt.EventsOn as any);
    } catch {
      // Headless test render — events stay unwired, chip still works
      // through the binding round-trip.
    }
    // Enumerate ports on mount + reflect any pre-existing connection
    // (the Wails backend keeps state across hot reloads in dev).
    await refreshPorts();
    await refreshStatus();
    // Same reload-survival for MIDI thru: the backend keeps
    // forwarding across a webview reload; reseed the chip from it.
    // Capabilities shape the dropdown (no virtual option on
    // Windows — WinMM can't create app-owned MIDI ports).
    await loadThruCaps();
    await syncThruStatus();
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
    <span class="chip__value">{probing ? 'Probing…' : ($selectedIn || '— pick —')}</span>
    <select value={currentInValue()} on:change={onPickIn} disabled={probing}>
      <option value="">— pick —</option>
      <!-- Probe: synthetic entry that runs auto-discovery instead of
           selecting a port. Hidden in MIDI-only setups (no serial
           devices means there's nothing to probe). Visible when at
           least one USB-serial candidate exists. -->
      {#if $serialPortsList.length > 0}
        <option value={PROBE}>🔍 Probe for S950… (~15s)</option>
      {/if}
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
  {:else if $phase === 'connected'}
    <!-- On serial the MIDI-out chip is gone; reuse that slot for a
         compact Refresh button. Only enabled when actually connected
         — refreshing offline would be a no-op + a confusing error
         modal. Hidden on MIDI because a full pull is multi-minute
         on MIDI's 3125 B/s line rate. -->
    <button
      type="button"
      class="chip chip--btn"
      on:click={openRefreshModal}
      title="Re-pull catalog, sample params, and all programs from the S950">
      ↻ Refresh
    </button>
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

  <!-- MIDI thru: forwards live MIDI down the serial wire. The S950
       ignores its DIN jacks entirely while controller-select is on
       RS-232C (hardware-verified), so this is the only way to play
       it from a DAW/keyboard mid-session. "Virtual port" publishes
       a CoreMIDI destination DAWs can target directly; the other
       entries tap existing inputs.
       Gated on the ACTUAL session kind ($status.kind), not the
       picker selection ($transportKind) — browsing the port picker
       flips the latter without disconnecting, and the chip must not
       vanish while forwarding is live. -->
  {#if $phase === 'connected' && $status.kind === 'serial'}
    <label
      class="chip chip--select"
      class:chip--thru-active={$midiThru.active}
      title={$midiThruError
        || ($midiThru.active
            ? `Forwarding ${$midiThru.source} → RS-232 (${$midiThru.forwarded} msgs${$midiThru.dropped ? `, ${$midiThru.dropped} dropped during transfers` : ''})`
            : ($thruCaps.virtualSupported
                ? 'Forward a MIDI input (or a virtual port your DAW can target) to the S950 over RS-232'
                : $thruCaps.hint || 'Forward a MIDI input to the S950 over RS-232'))}>
      <span class="chip__label">Thru</span>
      <span class="chip__value">{$midiThru.active ? `♪ ${thruSourceShort}` : 'off'}</span>
      <select value={thruSelectValue} disabled={thruBusy} on:change={onThruChange}>
        <option value="">off</option>
        {#if $thruCaps.virtualSupported}
          <option value={THRU_VIRTUAL}>Virtual port (for DAWs)</option>
        {/if}
        {#each $inputPorts as p}
          <option value={THRU_PORT + p.name}>{p.name}</option>
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
    <!-- System (Overall) settings — basic channel, omni, transmit
         channel, pitch-wheel range, etc. Gated on connected because
         the modal fetches OVS from the device on open. -->
    <button
      type="button"
      class="chip chip--btn"
      title="System settings (basic channel, omni, pitch-wheel range...)"
      on:click={openOverallModal}>
      ⚙ System
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

<!-- Refresh modal. Four phases: idle (start CTA), running (progress
     bar + cancel), done (✓ summary + close), error (message + close).
     Lives in the Topbar component so it's available on every page,
     since the trigger chip is in the topbar too. -->
{#if refreshModalOpen}
  <div class="modal-backdrop is-open" role="dialog" aria-modal="true">
    <div class="modal">
      <header class="modal__head">
        <h2 class="modal__title">
          {#if $refreshState.phase === 'done'}Refresh complete
          {:else if $refreshState.phase === 'error'}Refresh failed
          {:else if refreshRunning}Refreshing from S950
          {:else}Refresh from S950
          {/if}
        </h2>
        <div class="modal__route">full system state · programs + SPRM</div>
      </header>
      <div class="modal__body">
        {#if $refreshState.phase === 'idle'}
          <div class="modal__step">
            Pulls the device catalog, every program, and every sample's SPRM.
            Sample audio is pulled too by default — uncheck below for a fast
            metadata-only refresh when you know the audio hasn't changed
            (e.g. you only edited programs on the front panel).
          </div>
          <label class="modal__toggle">
            <input type="checkbox" bind:checked={refreshIncludeAudio} />
            <span>Include sample audio</span>
            <span class="modal__toggle-hint">
              {refreshIncludeAudio
                ? 'worst case ~10 min for a full EXM005; cached audio is skipped'
                : 'metadata only — seconds, not minutes'}
            </span>
          </label>
          <div class="modal__hint">
            Use this when the on-device state has drifted from what the app
            shows (e.g. you edited programs or recorded samples on the front
            panel).
          </div>
        {:else if refreshRunning}
          <div class="modal__step">{$refreshState.message}</div>
          <div class="progress">
            <div class="progress__bar" style="width: {$refreshState.progress}%;"></div>
            <div class="progress__label">
              {Math.floor($refreshState.progress)}%{#if $refreshState.subTotal > 0} · {$refreshState.subCurrent}/{$refreshState.subTotal}{/if}
            </div>
          </div>
          <div class="modal__hint">
            Don't close this window. Cancel stops at the next program boundary —
            in-flight requests always complete.
          </div>
        {:else if $refreshState.phase === 'done'}
          <div class="modal__step">✓ {$refreshState.message}</div>
          <div class="progress">
            <div class="progress__bar" style="width: 100%;"></div>
            <div class="progress__label">100%</div>
          </div>
        {:else if $refreshState.phase === 'error'}
          <div class="modal__step modal__step--error">
            {$refreshState.error ?? 'Unknown error'}
          </div>
        {/if}
      </div>
      <footer class="modal__foot">
        {#if $refreshState.phase === 'idle'}
          <button type="button" class="btn" on:click={closeRefreshModal}>Cancel</button>
          <button type="button" class="btn btn--primary" on:click={startRefresh}>Start refresh</button>
        {:else if refreshRunning}
          <span class="modal__waitnote">do not close · refresh in progress</span>
          <button type="button" class="btn" on:click={cancelRefresh}>Cancel</button>
        {:else}
          <button type="button" class="btn btn--primary" on:click={closeRefreshModal}>Close</button>
        {/if}
      </footer>
    </div>
  </div>
{/if}

<!-- RS-232 "sampler not responding" modal. Pops when connect() opened
     the serial port fine but the post-connect ping timed out. The
     OS-level open succeeds even when the S950 is in MIDI mode / off /
     on a wrong baud; this modal is the one place we explain the
     front-panel steps to recover. Close → stays disconnected so the
     user can fix the device and click Connect again. Try again →
     dismiss + re-run connect(); if it still fails the modal pops
     right back. -->
{#if $serialUnresponsive}
  <div class="modal-backdrop is-open" role="dialog" aria-modal="true">
    <div class="modal">
      <header class="modal__head">
        <h2 class="modal__title">S950 not responding over RS-232</h2>
        <div class="modal__route">serial port opened · no reply from sampler</div>
      </header>
      <div class="modal__body">
        <p class="modal__prose">
          The cable opened at {$baud} baud, but the S950 didn't reply to
          our test request within 2 seconds. Usually this means the
          sampler is still in MIDI mode, powered off, or set to a
          different baud rate.
        </p>
        <p class="modal__prose">On the S950's front panel:</p>
        <ol class="modal__list">
          <li>Press the MIDI button.</li>
          <li>Set CONTROLLER SELECT to RS-232C.</li>
          <li>Set the RS-232 baud to one of: 9600, 19200, 38400, or 50000.</li>
          <li>Confirm the cable is in the sampler's RS-232 port (not MIDI IN/OUT).</li>
        </ol>
        <p class="modal__prose modal__prose--hint">
          Make sure the Baud chip up top matches the baud on the sampler,
          then click Try again. Controller-select can only be changed
          from the front panel — the S950 silently ignores SysEx writes
          to that field, so we can't flip it for you.
        </p>
      </div>
      <footer class="modal__foot">
        <button type="button" class="btn" on:click={dismissSerialUnresponsive}>Close</button>
        <button
          type="button"
          class="btn btn--primary"
          on:click={() => { dismissSerialUnresponsive(); void connect(); }}>
          Try again
        </button>
      </footer>
    </div>
  </div>
{/if}

<!-- System (Overall) settings modal. Loads OVS on open, edits via
     bound form controls, writes back via SetOverall on Save. The
     OverallSettings.Raw bytes round-trip unchanged so fields we
     deliberately don't expose (ControllerSelect, BaudRate, RxSim*)
     keep their device-side values. -->
{#if overallModalOpen}
  <div class="modal-backdrop is-open" role="dialog" aria-modal="true">
    <div class="modal">
      <header class="modal__head">
        <h2 class="modal__title">
          {#if overallPhase === 'done'}Saved
          {:else if overallPhase === 'error'}Error
          {:else if overallPhase === 'saving'}Saving…
          {:else if overallPhase === 'loading'}Loading…
          {:else}System settings
          {/if}
        </h2>
        <div class="modal__route">overall settings · OVS block</div>
      </header>
      <div class="modal__body">
        {#if overallPhase === 'loading'}
          <p class="modal__prose">Fetching current settings from the S950…</p>
        {:else if overallPhase === 'error'}
          <div class="modal__step modal__step--error">{overallError || 'Unknown error'}</div>
        {:else if overallPhase === 'done'}
          <p class="modal__prose">Settings written to the S950.</p>
        {:else if overallEdit}
          <div class="ovs-grid">
            <label class="ovs-row">
              <span>Default program name</span>
              <input
                type="text"
                maxlength="10"
                value={(overallEdit.ProgName ?? '').trimEnd()}
                on:input={onProgNameInput} />
            </label>
            <label class="ovs-row">
              <span>Basic (receive) channel</span>
              <input type="number" min="0" max="15" bind:value={overallEdit.BasicChannel} />
            </label>
            <label class="ovs-row">
              <input type="checkbox" bind:checked={overallEdit.OmniOn} />
              <span>Omni on (receive on all channels — overrides basic channel)</span>
            </label>
            <label class="ovs-row">
              <span>Transmit channel (replies)</span>
              <input type="number" min="0" max="15" bind:value={overallEdit.MidiTxChannel} />
            </label>
            <label class="ovs-row">
              <span>Pitch wheel range (semitones)</span>
              <input type="number" min="0" max="12" bind:value={overallEdit.PitchWheelRange} />
            </label>
            <label class="ovs-row">
              <input type="checkbox" bind:checked={overallEdit.LoudnessOnCC7} />
              <span>Loudness on MIDI CC7</span>
            </label>
          </div>
          <p class="modal__prose modal__prose--hint">
            Controller-select (MIDI vs RS-232C) and the device's
            RS-232 baud are set on the S950's front panel — firmware
            silently ignores SysEx writes to those fields, so they
            aren't exposed here. Their current device values are
            preserved unchanged when you save.
          </p>
        {/if}

        <!-- Host-side maintenance. Rendered outside the OVS phase
             branches so it stays reachable when the sampler is
             disconnected (GetOverall error) — clearing the disk
             cache never needs the device. Hidden only while an OVS
             read/write is in flight to avoid stray clicks. -->
        {#if overallPhase !== 'loading' && overallPhase !== 'saving'}
          <div class="cache-tools">
            <div class="cache-tools__head">Host waveform cache</div>
            <p class="modal__prose modal__prose--hint">
              Audio copied from the S950 is cached on this computer so
              the next session shows real waveforms without
              re-downloading. Clearing it doesn't touch the device or
              the current session — the next Refresh re-pulls audio
              from the sampler.
            </p>
            {#if cachePhase === 'confirm'}
              <div class="cache-tools__row">
                <button type="button" class="btn btn--danger" on:click={clearWaveCache}>
                  Confirm clear
                </button>
                <button type="button" class="btn" on:click={() => (cachePhase = 'idle')}>
                  Keep
                </button>
              </div>
            {:else if cachePhase === 'clearing'}
              <span class="modal__waitnote">clearing…</span>
            {:else if cachePhase === 'done'}
              <span class="cache-tools__done">✓ cache cleared — run Refresh (with audio) to re-pull from the S950</span>
            {:else if cachePhase === 'error'}
              <div class="modal__step modal__step--error">{cacheError || 'Unknown error'}</div>
            {:else}
              <button type="button" class="btn" on:click={() => (cachePhase = 'confirm')}>
                Clear waveform cache…
              </button>
            {/if}
          </div>
        {/if}
      </div>
      <footer class="modal__foot">
        {#if overallPhase === 'editing'}
          <button type="button" class="btn" on:click={closeOverallModal}>Cancel</button>
          <button type="button" class="btn btn--primary" on:click={saveOverall}>Save to S950</button>
        {:else if overallPhase === 'saving' || overallPhase === 'loading'}
          <span class="modal__waitnote">do not close · in progress</span>
        {:else}
          <button type="button" class="btn btn--primary" on:click={closeOverallModal}>Close</button>
        {/if}
      </footer>
    </div>
  </div>
{/if}

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
  /* THRU chip while forwarding — accent border + value so "live
     MIDI is flowing down the wire" reads at a glance, same visual
     language as the synced status dot. */
  :global(.chip--thru-active) {
    border-color: var(--rb-yellow);
  }
  :global(.chip--thru-active .chip__value) {
    color: var(--rb-yellow);
    font-weight: 700;
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

  /* Modal-body toggle row used by the Refresh "include audio"
     checkbox. Sits between the explanation paragraph and the
     hint footer; hint text under the label gives a live ETA
     summary so the user sees the impact of toggling. */
  :global(.modal__toggle) {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 12px 0;
    cursor: pointer;
    user-select: none;
  }
  :global(.modal__toggle input[type="checkbox"]) {
    margin: 0;
    cursor: pointer;
  }
  :global(.modal__toggle-hint) {
    color: var(--grey-medium);
    font-size: 11px;
    margin-left: auto;
  }

  /* "S950 not responding" modal body — prose + ordered list. The
     existing .modal__step rules turn <strong> into a block-level
     section label, which doesn't fit instructional copy; these
     classes are a plain mono paragraph + numbered list at the
     same visual altitude as .modal__step text. */
  :global(.modal__prose) {
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.5;
    color: var(--grey-dark);
    margin: 0 0 10px;
  }
  :global(.modal__prose--hint) {
    color: var(--grey-medium);
    font-size: 11px;
    margin-top: 10px;
  }
  :global(.modal__list) {
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.5;
    color: var(--black);
    margin: 0 0 10px;
    padding-left: 22px;
  }
  :global(.modal__list li) {
    margin: 4px 0;
  }

  /* OVS form grid — one row per field, label on the left and input
     on the right. Inputs are minimal native controls; sized small
     enough that 7+ rows fit in the 540px modal without scroll. */
  :global(.ovs-grid) {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin: 4px 0 12px;
  }
  :global(.ovs-row) {
    display: flex;
    align-items: center;
    gap: 10px;
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--black);
    cursor: pointer;
  }
  :global(.ovs-row > span:first-child),
  :global(.ovs-row > span:nth-child(2)) {
    flex: 1;
    /* Defensive — long inline labels ("Omni on (receive on all
       channels — overrides basic channel)") want to grow the row
       past the modal's max-width:92vw on very narrow viewports
       unless the flex item is allowed to shrink below min-content. */
    min-width: 0;
  }
  :global(.ovs-row input[type="number"]),
  :global(.ovs-row input[type="text"]) {
    font: inherit;
    width: 9ch;
    padding: 2px 6px;
    border: 1px solid var(--grey-medium);
    border-radius: 3px;
    background: var(--white);
    color: var(--black);
  }
  :global(.ovs-row input[type="text"]) {
    width: 14ch;
  }
  :global(.ovs-row input[type="checkbox"]) {
    margin: 0;
    cursor: pointer;
  }

  /* Host-maintenance section at the bottom of the System modal —
     visually separated from the device-side OVS form by a rule, so
     it reads as "this part is about your computer, not the S950". */
  :global(.cache-tools) {
    margin-top: 14px;
    padding-top: 12px;
    border-top: 1px solid var(--grey-light);
  }
  :global(.cache-tools__head) {
    font-family: var(--font-mono);
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--grey-dark);
    margin-bottom: 4px;
  }
  :global(.cache-tools__row) {
    display: flex;
    gap: 8px;
  }
  :global(.cache-tools__done) {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--grey-dark);
  }
</style>
