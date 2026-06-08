<script lang="ts">
  import Topbar from '../lib/Topbar.svelte';
  import Statusbar from '../lib/Statusbar.svelte';
  import NumField from '../lib/NumField.svelte';
  import { setSync } from '../lib/sync';
  import { navigate } from '../lib/route';
  import {
    programs,
    selectedSlot,
    selectedProgram,
    selectedKeygroupN,
    newLocalProgram,
    pickFreeProgramSlot,
    isAssignedSample,
    type Program,
  } from '../lib/state/programs';
  import { ensureProgramLoaded, programJSONToProgram } from '../lib/state/catalog';
  import { status as connectionStatus } from '../lib/state/connection';
  import { rangeLabel, midiX, midiW } from '../lib/midi';
  import { get } from 'svelte/store';
  import { onMount } from 'svelte';
  import * as App from '../../wailsjs/go/main/App';
  import { programToJSON } from '../lib/state/livesync';

  // Connection identifier for modal subtitles / log lines. Mirrors
  // the same helper in Sample.svelte: "<port> · Ch <n>" on MIDI,
  // bare port on serial.
  $: connectionLabel = (() => {
    const s = $connectionStatus;
    if (!s.connected) return 'Not connected';
    if (s.kind === 'serial') return s.in ?? 'serial';
    return `${s.in ?? '?'} · Ch ${s.channel}`;
  })();

  // Program is the explicit-Send tab — local edits are dirty until the
  // user presses Send. Real impl will flip to 'synced' on a successful
  // SetProgram round-trip.
  onMount(() => setSync('dirty', 'Unsaved'));

  // ---------- Modal flow ----------
  type ModalKind = 'closed' | 'preflight' | 'transfer';
  let modalKind: ModalKind = 'closed';
  function openSend()     { modalKind = 'preflight'; }
  function continueSend() { modalKind = 'transfer'; }
  function cancel()       { modalKind = 'closed'; }

  // Get from S950: re-pull the currently selected program from the
  // device. No modal — fast operation; surface progress via the
  // topbar sync chip. Failure caught + shown via the chip's error
  // state.
  async function getFromDevice() {
    const slot = get(selectedSlot);
    setSync('sending', 'Fetching...');
    try {
      await ensureProgramLoaded(slot, true);
      setSync('synced', 'Synced');
    } catch (e: any) {
      setSync('error', 'Fetch failed');
    }
  }

  function editKeygroup(n: number) {
    selectedKeygroupN.set(n);
    navigate('keygroup');
  }

  const padSlot = (n: number) => n.toString().padStart(2, '0');

  function onNameInput(e: Event) {
    selectedProgram.update({ name: (e.target as HTMLInputElement).value });
  }

  // EMPTY_PROGRAM is the type-safe fallback while the sidebar is
  // empty (fresh app, no Connect, no New). Reactive derivations need
  // a defined Program; the actual empty-state UI is gated by
  // hasProgram so the user sees the CTA, not these zeroed values.
  const EMPTY_PROGRAM: Program = {
    slot: -1, name: '', midiProg: 1, respondPC: false,
    keyTilt: 0, positionalXfade: false, keygroups: [],
  };
  $: prog = $selectedProgram ?? EMPTY_PROGRAM;
  $: hasProgram = !!$selectedProgram;

  // Allocate a free slot and seed a local-only Program. The user can
  // then edit and either Send to S950 or Save .json.
  function newProgram() {
    const list = get(programs);
    const slot = pickFreeProgramSlot(list);
    if (slot < 0) {
      setSync('error', 'All 100 program slots are occupied');
      return;
    }
    const p = newLocalProgram(slot, '');
    programs.update((xs) => [...xs, p].sort((a, b) => a.slot - b.slot));
    selectedSlot.set(slot);
    selectedKeygroupN.set(1);
    setSync('dirty', 'Unsaved');
  }

  // Save / Open .json: minimal scope — programs only, no embedded
  // sample audio. The on-disk shape matches the CLI's put-program
  // / get-program, so a JSON saved here uploads with `s950-tools
  // put-program file.json` and vice versa. Bundled-sample "patch"
  // archives are an unbuilt larger feature; the user manages WAVs
  // separately for now.
  async function saveProgramJSON() {
    if (!hasProgram) return;
    try {
      const json = programToJSON(prog);
      const suggested = (prog.name || `program-${prog.slot}`).trim() + '.json';
      const written = await App.SaveProgramJSON(json as any, suggested);
      if (written) setSync('synced', 'Saved');
    } catch (e: any) {
      setSync('error', 'Save failed: ' + String(e?.message ?? e));
    }
  }
  async function openProgramJSON() {
    try {
      const json = await App.OpenProgramJSON();
      if (!json) return; // user cancelled
      const list = get(programs);
      const slot = pickFreeProgramSlot(list);
      if (slot < 0) {
        setSync('error', 'All 100 program slots are occupied');
        return;
      }
      const loaded = programJSONToProgram(json as any, slot);
      programs.update((xs) => [...xs, loaded].sort((a, b) => a.slot - b.slot));
      selectedSlot.set(slot);
      selectedKeygroupN.set(1);
      // Mark dirty — JSON was loaded into a LOCAL slot, not pulled
      // from the device. Send to S950 (or Save .json again to a
      // different file) commits the user's intent from here.
      setSync('dirty', 'Loaded from JSON · unsaved on device');
    } catch (e: any) {
      setSync('error', 'Open failed: ' + String(e?.message ?? e));
    }
  }

  // Duplicate the currently-selected program into the next free
  // slot. Keygroups + all editable fields are deep-cloned so future
  // edits to the copy don't bleed back into the original. The new
  // row lands as `DUP <orig name>` so it's easy to spot in the
  // sidebar; the user renames after if they want something cleaner.
  function duplicateProgram() {
    if (!hasProgram) return;
    const src = prog;
    const list = get(programs);
    const slot = pickFreeProgramSlot(list);
    if (slot < 0) {
      setSync('error', 'All 100 program slots are occupied');
      return;
    }
    const dup: Program = JSON.parse(JSON.stringify(src));
    dup.slot = slot;
    // Prepend "DUP " and clip to the S950's 10-char name limit.
    dup.name = ('DUP ' + (src.name ?? '')).slice(0, 10);
    programs.update((xs) => [...xs, dup].sort((a, b) => a.slot - b.slot));
    selectedSlot.set(slot);
    selectedKeygroupN.set(1);
    setSync('dirty', 'Unsaved');
  }
</script>

<div class="app app--3row">
  <Topbar slotCount={`${$programs.length} / 100`} slotNoun="programs" />

  <!-- Programs sidebar -->
  <aside class="sidebar">
    <div class="sidebar__panel">
      <div class="sidebar__head">
        <div class="sidebar__title">Programs</div>
        <div class="sidebar__count">{$programs.length} / 100</div>
      </div>
      <div class="program-list">
        {#each $programs as p (p.slot)}
          <div
            class="program {p.slot === $selectedSlot ? 'selected' : ''}"
            on:click={() => selectedSlot.set(p.slot)}
            on:keydown={(e) => e.key === 'Enter' && selectedSlot.set(p.slot)}
            role="button"
            tabindex="0">
            <span class="program__slot">{padSlot(p.slot)}</span>
            <span class="program__name">{p.name || '(unnamed)'}</span>
            <span class="program__kg">{p.keygroups.length} kg</span>
          </div>
        {/each}
        {#if $programs.length === 0}
          <div class="sample-list__empty">
            No programs.<br/>Connect to S950 or click New program.
          </div>
        {/if}
      </div>
      <div class="sidebar__hint">
        drop .json program file here<br/>or click + to create
      </div>
    </div>
  </aside>

  <!-- Main editor pane.
       Top row: identity (left) + mini piano-roll overview (right) so
       the wide screen isn't half empty. Below: keygroups table (with
       its own scroll), then device toolbar pinned to the bottom.
       When the sidebar is empty an empty-state CTA replaces the cards
       so the user sees what to do first. -->
  <main class="main">
    {#if !hasProgram}
      <div class="empty-state">
        <div class="empty-state__title">No programs yet</div>
        <p class="empty-state__hint">
          Connect to your S950 to load its program catalog, or start a
          new program from scratch.
        </p>
        <button type="button" class="btn btn--primary" on:click={newProgram}>
          New program
        </button>
      </div>
    {:else}
    <section class="card card--form top-form">
      <div class="card__head">
        <div class="card__title">Program</div>
        <div class="card__subtitle">
          slot {padSlot(prog.slot)} · {prog.keygroups.length} keygroups
        </div>
      </div>

      <div class="row">
        <span class="row__label">Name</span>
        <span class="field field--wide field--yellow">
          <input
            type="text"
            value={prog.name}
            on:input={onNameInput}
            maxlength="12" />
        </span>
      </div>
      <div class="row">
        <span class="row__label">Slot</span>
        <NumField
          value={prog.slot}
          min={0} max={99}
          format={padSlot}
          on:change={(e) => selectedProgram.update({ slot: e.detail })} />
      </div>
      <div class="row">
        <span class="row__label">MIDI prog #</span>
        <NumField
          value={prog.midiProg}
          min={1} max={128}
          format={(v) => v.toString().padStart(3, '0')}
          on:change={(e) => selectedProgram.update({ midiProg: e.detail })} />
      </div>
      <div class="row">
        <span class="row__label">Respond to PC</span>
        <button
          type="button"
          class="toggle {prog.respondPC ? 'on' : ''}"
          on:click={() => selectedProgram.update({ respondPC: !prog.respondPC })}>
          {prog.respondPC ? 'Enabled' : 'Disabled'}
        </button>
      </div>

      <details class="advanced">
        <summary>Advanced</summary>
        <div class="row">
          <span class="row__label">Key tilt</span>
          <NumField
            value={prog.keyTilt}
            min={-64} max={63}
            on:change={(e) => selectedProgram.update({ keyTilt: e.detail })} />
        </div>
        <div class="row">
          <span class="row__label">Positional xfade</span>
          <button
            type="button"
            class="toggle {prog.positionalXfade ? 'on' : ''}"
            on:click={() => selectedProgram.update({ positionalXfade: !prog.positionalXfade })}>
            {prog.positionalXfade ? 'On' : 'Off'}
          </button>
        </div>
      </details>
    </section>

    <!-- Read-only piano-roll preview. Same coordinate system as the
         Keygroup canvas, so the two views feel related. Clicking a
         zone here jumps into the Keygroup editor with that one
         selected. -->
    <section class="card preview">
      <div class="card__head">
        <div class="card__title">Layout</div>
        <div class="card__subtitle">click a zone to edit</div>
      </div>
      <div class="kbroll">
        <div class="kbroll__keys" aria-hidden="true"></div>
        <div class="kbroll__zones">
          {#each prog.keygroups as kg (kg.n)}
            <span
              class="kbroll__zone"
              style="left: {midiX(kg.lowKey).toFixed(2)}%; width: {midiW(kg.lowKey, kg.highKey).toFixed(2)}%; --zone-bg: var({kg.color});"
              title={`${kg.soft.sample} · ${rangeLabel(kg.lowKey, kg.highKey)}`}
              on:click={() => editKeygroup(kg.n)}
              on:keydown={(e) => e.key === 'Enter' && editKeygroup(kg.n)}
              role="button"
              tabindex="0"></span>
          {/each}
        </div>
      </div>
      <div class="kbroll__legend">
        {#each prog.keygroups as kg (kg.n)}
          <span><span class="kg-swatch" style="background: var({kg.color})"></span>{kg.soft.sample || `kg ${kg.n}`}</span>
        {/each}
      </div>
    </section>

    <section class="card kg-card">
      <div class="card__head">
        <div class="card__title">Keygroups ({prog.keygroups.length})</div>
        <div class="card__subtitle">jump into Keygroup to edit a row</div>
      </div>
      <div class="kg-table-wrap">
      <table class="kg-table">
        <thead>
          <tr>
            <th style="width: 36px;">#</th>
            <th style="width: 140px;">Range</th>
            <th style="width: 100px;">Vel switch</th>
            <th>Soft sample</th>
            <th>Loud sample</th>
            <th style="width: 90px;"></th>
          </tr>
        </thead>
        <tbody>
          {#each prog.keygroups as kg (kg.n)}
            <tr>
              <td class="mono">{kg.n}</td>
              <td class="mono">
                <span class="kg-swatch" style="background: var({kg.color})"></span>{rangeLabel(kg.lowKey, kg.highKey)}
              </td>
              <td class="mono">{kg.vel}</td>
              <td class:none={!isAssignedSample(kg.soft.sample)}>{isAssignedSample(kg.soft.sample) ? kg.soft.sample : '(none)'}</td>
              <td class:none={!isAssignedSample(kg.loud.sample)}>{isAssignedSample(kg.loud.sample) ? kg.loud.sample : '(none)'}</td>
              <td>
                <button type="button" class="kg-link" on:click={() => editKeygroup(kg.n)}>Edit ↗</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
      </div>
    </section>

    <section class="card device-card">
      <div class="card__head">
        <div class="card__title">Device</div>
        <div class="card__subtitle">{connectionLabel}</div>
      </div>
      <div class="actions">
        <div class="actions__group">
          <button type="button" class="btn" on:click={getFromDevice}>Get from S950</button>
          <button type="button" class="btn btn--primary" on:click={openSend}>Send to S950</button>
        </div>
        <div class="actions__sep"></div>
        <div class="actions__group">
          <button type="button" class="btn" on:click={openProgramJSON}>Open .json...</button>
          <button type="button" class="btn" on:click={saveProgramJSON} disabled={!hasProgram}>Save .json...</button>
        </div>
        <div class="actions__sep"></div>
        <div class="actions__group">
          <button type="button" class="btn" on:click={newProgram}>New program</button>
          <button type="button" class="btn" on:click={duplicateProgram} disabled={!hasProgram}>Duplicate</button>
        </div>
        <span class="hint">last synced 2 min ago</span>
      </div>
    </section>
    {/if}
  </main>

  <Statusbar
    hints={[
      { key: '↵',  label: 'edit name' },
      { key: '⌘S', label: 'save .json' },
      { key: '⌘↵', label: 'send to s950' },
    ]}
    status={hasProgram
      ? `Program slot ${padSlot(prog.slot)} · ${prog.keygroups.length} / 31 keygroups used`
      : 'no program selected'}
  />
</div>

<!-- Modals -->
{#if modalKind === 'preflight'}
  <div class="modal-backdrop is-open" role="dialog" aria-modal="true">
    <div class="modal">
      <header class="modal__head">
        <h2 class="modal__title">Ready to send <strong>{prog.name}</strong></h2>
        <div class="modal__route">{connectionLabel}</div>
      </header>
      <div class="modal__body">
        <ul class="preflight">
          <li class="preflight__item preflight__item--ok">
            <span class="preflight__icon">✓</span>
            <div class="preflight__body">
              <span class="preflight__title">Connection</span>
              <span class="preflight__detail">{connectionLabel} · S950 responded to catalog request</span>
            </div>
          </li>
          <li class="preflight__item preflight__item--ok">
            <span class="preflight__icon">✓</span>
            <div class="preflight__body">
              <span class="preflight__title">Keygroup count</span>
              <span class="preflight__detail">{prog.keygroups.length} keygroups · max 31</span>
            </div>
          </li>
          <li class="preflight__item preflight__item--ok">
            <span class="preflight__icon">✓</span>
            <div class="preflight__body">
              <span class="preflight__title">Free sample memory</span>
              <span class="preflight__detail">52,400 / 475,020 words needed · 422,620 free after upload</span>
            </div>
          </li>
          <li class="preflight__item preflight__item--warn">
            <span class="preflight__icon">⚠</span>
            <div class="preflight__body">
              <span class="preflight__title">Slot {padSlot(prog.slot)} occupied (OLD KIT)</span>
              <span class="preflight__detail">Overwriting will replace the existing program on the device.</span>
              <div class="preflight__action">
                <button type="button" class="btn">Overwrite</button>
                <button type="button" class="btn">Use slot 07 (empty)</button>
              </div>
            </div>
          </li>
          <li class="preflight__item preflight__item--warn">
            <span class="preflight__icon">⚠</span>
            <div class="preflight__body">
              <span class="preflight__title">Sample KICK referenced elsewhere</span>
              <span class="preflight__detail">PIANO (slot 05) uses this sample. Overwriting it will change PIANO's sound.</span>
            </div>
          </li>
        </ul>
        <div class="preflight__estimate">
          Estimated transfer: 4 samples · ~3m 04s
        </div>
      </div>
      <footer class="modal__foot">
        <button type="button" class="btn" on:click={cancel}>Cancel</button>
        <button type="button" class="btn btn--primary" on:click={continueSend}>Continue ▶</button>
      </footer>
    </div>
  </div>
{:else if modalKind === 'transfer'}
  <div class="modal-backdrop is-open" role="dialog" aria-modal="true">
    <div class="modal">
      <header class="modal__head">
        <h2 class="modal__title">Sending <strong>{prog.name}</strong> to S950</h2>
        <div class="modal__route">{connectionLabel}</div>
      </header>
      <div class="modal__body">
        <div class="modal__step">
          Step 2 of 4 · Uploading sample
          <strong>SNARE → slot 03 (12.4 / 24.1 KB)</strong>
        </div>
        <div class="progress">
          <div class="progress__bar" style="width: 51%;"></div>
          <div class="progress__label">51%</div>
        </div>
        <div class="modal__times">
          <span>Elapsed 1m 12s</span>
          <span>~2m 18s remaining</span>
        </div>
        <div class="log">
          <div class="log__row ok">✓ Connected to S950 on {connectionLabel}</div>
          <div class="log__row ok">✓ Sample KICK uploaded to slot 02 (1m 04s)</div>
          <div class="log__row run">▶ Sample SNARE in progress</div>
          <div class="log__row pending">· Sample RIM pending</div>
          <div class="log__row pending">· Program {prog.name} pending</div>
        </div>
      </div>
      <footer class="modal__foot">
        <button type="button" class="btn" on:click={cancel}>Cancel transfer</button>
      </footer>
    </div>
  </div>
{/if}

<style>
  /* Page-specific only — shared shell + cards + form + modals come
     from shared.css. */
  .main {
    grid-area: main;
    overflow: auto;
    padding: 16px;
    display: grid;
    grid-template-columns: minmax(420px, 560px) 1fr;
    grid-template-rows: auto 1fr auto;
    gap: 16px;
    background: var(--main-bg);
    min-height: 0;
  }
  /* Empty-state spans the whole grid so the dashed CTA panel fills
     the work area instead of getting squeezed into the first cell. */
  .main > .empty-state { grid-column: 1 / -1; grid-row: 1 / -1; }
  .top-form     { grid-column: 1; grid-row: 1; }
  .preview      { grid-column: 2; grid-row: 1; }
  /* min-height keeps the keygroups card from collapsing below ~4
     rows when the viewport is short — without it, the 1fr grid
     track can shrink to 0 and the card's overflow:hidden then
     clips the entire table body. .main has overflow:auto so the
     page scrolls vertically when the viewport can't fit it all. */
  .kg-card      { grid-column: 1 / -1; grid-row: 2; min-height: 220px; }
  .device-card  { grid-column: 1 / -1; grid-row: 3; }
  .card--form { max-width: 560px; }

  /* Scrollable wrapper so 31-keygroup programs don't push the Device
     toolbar off-screen. */
  .kg-card { display: flex; flex-direction: column; }
  .kg-table-wrap {
    overflow-y: auto;
    /* x-scroll too so long sample names + the Edit ↗ button don't
       get silently clipped by the parent .card's overflow: hidden
       at narrow widths (notably the stacked layout below 1100px). */
    overflow-x: auto;
    max-height: 360px;
    margin: 0 -2px;
  }

  /* Below ~1100px the 420px first column dominates and the second
     (Layout) column gets squeezed off-screen. Stack to a single
     column and re-flow the grid areas. */
  @media (max-width: 1100px) {
    .main {
      grid-template-columns: 1fr;
      grid-template-rows: auto auto 1fr auto;
    }
    .top-form     { grid-column: 1; grid-row: 1; }
    .preview      { grid-column: 1; grid-row: 2; }
    .kg-card      { grid-column: 1; grid-row: 3; }
    .device-card  { grid-column: 1; grid-row: 4; }
    .card--form { max-width: none; }
  }

  /* ---------- Mini piano roll (read-only preview) ---------- */
  .kbroll {
    position: relative;
    height: 56px;
    border: var(--bw) solid var(--black);
    border-radius: var(--r);
    background: var(--canvas);
    overflow: hidden;
  }
  .kbroll__keys {
    position: absolute; inset: 0;
    background:
      repeating-linear-gradient(to right,
        rgba(255,255,255,0.08) 0 calc(100% / 88 * 12 - 1px),
        rgba(255,255,255,0.18) calc(100% / 88 * 12 - 1px) calc(100% / 88 * 12));
  }
  .kbroll__zones {
    position: absolute; inset: 0;
  }
  .kbroll__zone {
    position: absolute;
    top: 6px; bottom: 6px;
    background: var(--zone-bg, var(--rb-yellow));
    border: 1px solid var(--black);
    border-radius: 2px;
    cursor: pointer;
    opacity: 0.85;
  }
  .kbroll__zone:hover { opacity: 1; outline: 2px solid var(--rb-yellow); }
  .kbroll__legend {
    display: flex;
    flex-wrap: wrap;
    gap: 8px 14px;
    margin-top: 10px;
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--grey-dark);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .kbroll__legend :global(span) { display: inline-flex; align-items: center; gap: 4px; }

  .kg-table { width: 100%; border-collapse: collapse; font-size: 13px; }
  .kg-table :global(th),
  .kg-table :global(td) {
    padding: 8px 10px;
    text-align: left;
    border-bottom: 1px solid var(--grey-light);
  }
  .kg-table :global(th) {
    font-family: var(--font-mono);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-size: 10px;
    color: var(--grey-dark);
    border-bottom: var(--bw) solid var(--black);
  }
  .kg-table :global(td.mono) { font-family: var(--font-mono); font-size: 12px; }
  .kg-table :global(td.none) { color: var(--grey-medium); }
  .kg-table :global(tr:hover td) { background: var(--grey-light); }
  .kg-swatch {
    display: inline-block;
    width: 10px; height: 10px;
    border-radius: 2px;
    border: 1px solid var(--black);
    margin-right: 6px;
    vertical-align: -1px;
  }
  .kg-link {
    font-family: var(--font-mono);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--black);
    border: 1px solid var(--black);
    border-radius: 3px;
    padding: 2px 8px;
    background: var(--white);
    cursor: pointer;
  }
  .kg-link:hover { background: var(--rb-yellow); color: var(--ink); }

  .actions {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .actions__group { display: inline-flex; gap: 6px; }
  .actions__sep {
    width: 1px;
    height: 24px;
    background: var(--grey-medium);
    margin: 0 4px;
  }
  .hint {
    /* Always land on its own row at the right edge instead of fighting
       for inline space with the action buttons — margin-left: auto
       used to push it solo to a wrapped row anyway, which read as
       broken. Pin it explicitly so the layout intent is clear. */
    flex-basis: 100%;
    text-align: right;
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--grey-dark);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  details.advanced { margin-top: 8px; }
  details.advanced :global(summary) {
    font-family: var(--font-mono);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-size: 10px;
    color: var(--grey-dark);
    cursor: pointer;
    list-style: none;
    padding: 6px 0;
    border-top: 1px solid var(--grey-light);
  }
  /* WebKit (Wails on macOS) still paints the native disclosure
     triangle unless ::-webkit-details-marker is explicitly hidden;
     list-style:none alone isn't enough. Otherwise the row reads
     '▶ Advanced ▸' with both the native + custom chevrons. */
  details.advanced :global(summary)::-webkit-details-marker {
    display: none;
  }
  details.advanced :global(summary)::after { content: " ▸"; }
  details.advanced[open] :global(summary)::after { content: " ▾"; }
</style>
