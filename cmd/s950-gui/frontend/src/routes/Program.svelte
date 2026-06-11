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
  import { ensureProgramLoaded, programJSONToProgram, markProgramOnDevice } from '../lib/state/catalog';
  import { status as connectionStatus } from '../lib/state/connection';
  import { rangeLabel, midiX, midiW } from '../lib/midi';
  import { get } from 'svelte/store';
  import { onMount } from 'svelte';
  import * as App from '../../wailsjs/go/main/App';
  import { programToJSON } from '../lib/state/livesync';
  import { samples } from '../lib/state/samples';
  import { sampleToSampleParams, sampleParamsToSample } from '../lib/state/converters';
  import { wordsToPcm } from '../lib/state/waveformcache';
  import { planProgramSend, type SendPlan } from '../lib/state/sendplan';
  import {
    onProgramSelected, invalidateActivation,
    lastActivation, followBlockedReason, followEnabled,
  } from '../lib/state/followsync';
  import { memoryUsage, totalWords } from '../lib/state/memory';
  import { baud } from '../lib/state/connection';

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
  function cancel()       { modalKind = 'closed'; }

  // openSend computes the REAL preflight plan from the stores —
  // never-overwrite policy, missing-sample detection, free-memory
  // check — and shows it. Continue is disabled while plan.ok is
  // false. (This modal used to be static mockup with dead
  // Overwrite/Use-slot buttons; overwriting occupied S950 slots is
  // unreliable on hardware, so the planner re-targets instead of
  // asking.)
  let sendPlan: SendPlan | null = null;
  function openSend() {
    if (!hasProgram) return;
    sendPlan = planProgramSend(
      prog,
      get(programs),
      get(samples),
      Math.max(0, get(totalWords) - get(memoryUsage).usedWords),
      get(baud) || 50000,
    );
    modalKind = 'preflight';
  }

  // continueSend executes the plan: upload each missing referenced
  // sample to its assigned FREE slot, then write the program to its
  // resolved slot, then promote everything to 'device' so the lazy
  // loader / live-sync apply again. Per-step progress renders in
  // the transfer modal.
  type SendStep = { label: string; status: 'pending' | 'run' | 'ok' | 'fail' };
  let sendSteps: SendStep[] = [];
  type SendPhase = 'sending' | 'done' | 'error';
  let sendPhase: SendPhase = 'sending';
  let sendError = '';

  async function continueSend() {
    if (!sendPlan || !sendPlan.ok) return;
    const plan = sendPlan;
    modalKind = 'transfer';
    sendPhase = 'sending';
    sendError = '';
    sendSteps = [
      ...plan.uploads.map((u) => ({
        label: `Sample ${u.name} → slot ${padSlot(u.toSlot)}`,
        status: 'pending' as const,
      })),
      { label: `Program ${prog.name || '(unnamed)'} → slot ${padSlot(plan.programSlot)}`, status: 'pending' as const },
    ];
    const setStep = (i: number, status: SendStep['status']) => {
      sendSteps = sendSteps.map((s, j) => (j === i ? { ...s, status } : s));
    };

    try {
      for (let i = 0; i < plan.uploads.length; i++) {
        const u = plan.uploads[i];
        setStep(i, 'run');
        const row = get(samples).find((s) => s.slot === u.fromSlot);
        if (!row || !row.words12) throw new Error(`sample ${u.name} disappeared from the store`);
        // Reassign the row to its device slot first so the SPRM we
        // send carries consistent state. The device slot is the
        // LOWEST free one (the S950 appends there regardless of
        // what the dump header asks for), which may collide with
        // another local row in the store — swap the two rows so
        // both survive with unique slots.
        if (u.toSlot !== u.fromSlot) {
          samples.update((xs) => xs.map((s) => {
            if (s.slot === u.fromSlot) return { ...s, slot: u.toSlot };
            if (s.slot === u.toSlot) return { ...s, slot: u.fromSlot };
            return s;
          }));
        }
        const sent = get(samples).find((s) => s.slot === u.toSlot)!;
        await (App as any).SendSample(u.toSlot, sent.words12, sent.rate, sampleToSampleParams(sent));
        // Promote: this slot now mirrors the device.
        samples.update((xs) => xs.map((s) =>
          s.slot === u.toSlot ? { ...s, source: 'device' as const } : s));
        setStep(i, 'ok');
      }

      const progStep = plan.uploads.length;
      setStep(progStep, 'run');
      // Apply the slot reassignment (never-overwrite policy) before
      // encoding.
      if (plan.slotReassigned && plan.programSlot !== prog.slot) {
        const oldSlot = prog.slot;
        programs.update((xs) => xs.map((p) =>
          p.slot === oldSlot ? { ...p, slot: plan.programSlot } : p));
        selectedSlot.set(plan.programSlot);
      }
      const toSend = get(programs).find((p) => p.slot === plan.programSlot);
      if (!toSend) throw new Error('program disappeared from the store');
      await App.SetProgram(plan.programSlot, programToJSON(toSend) as any);
      markProgramOnDevice(plan.programSlot);
      setStep(progStep, 'ok');
      sendPhase = 'done';
      setSync('synced', 'Synced');
    } catch (e: any) {
      sendSteps = sendSteps.map((s) => (s.status === 'run' ? { ...s, status: 'fail' } : s));
      sendPhase = 'error';
      sendError = String(e?.message ?? e);
      setSync('error', 'Send failed');
    }
  }

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
    source: 'local',
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

  // The Gotek/FlashFloppy .img IS the app's patch format: it
  // captures programs + keygroups + samples + overall settings
  // bit-exact in the device's own serialization, loads back here,
  // boots a real S950, and reads in akaiutil. Program-only JSON
  // save/open used to live here too — removed deliberately (a
  // program without its samples is half a patch); the JSON shape
  // remains a CLI-only interop format (put-program / get-program).
  // Open Gotek image: load a floppy image's programs + samples into
  // the app as LOCAL entries (they're on a disk file, not on the
  // device — Send/Apply commits them to hardware, or Export writes
  // them back out). Free slots are assigned client-side; capacity
  // is pre-checked so a too-full session fails before any store
  // mutation instead of half-loading.
  let importBusy = false;
  async function openGotekImage() {
    if (importBusy) return;
    importBusy = true;
    try {
      const res = await (App as any).OpenGotekImage();
      if (!res) return; // user cancelled
      const sampleRows = get(samples);
      const progRows = get(programs);
      const usedSampleSlots = new Set(sampleRows.map((s) => s.slot));
      const usedProgSlots = new Set(progRows.map((p) => p.slot));
      const freeSamples: number[] = [];
      const freeProgs: number[] = [];
      for (let i = 0; i < 100; i++) {
        if (!usedSampleSlots.has(i)) freeSamples.push(i);
        if (!usedProgSlots.has(i)) freeProgs.push(i);
      }
      const nS = (res.samples ?? []).length;
      const nP = (res.programs ?? []).length;
      if (nS > freeSamples.length || nP > freeProgs.length) {
        setSync('error',
          `Image needs ${nS} sample + ${nP} program slots; only ${freeSamples.length}/${freeProgs.length} free`);
        return;
      }

      const newSamples = (res.samples ?? []).map((imp: any, i: number) => {
        const slot = freeSamples[i];
        const row = sampleParamsToSample(imp.params, slot);
        return {
          ...row,
          words12: imp.words as number[],
          pcm: wordsToPcm(imp.words as number[]),
          // On a disk file, not on the device.
          source: 'local' as const,
          originalSource: 'local' as const,
        };
      });
      const newProgs = (res.programs ?? []).map((pj: any, i: number) =>
        programJSONToProgram(pj, freeProgs[i], 'local'));

      if (newSamples.length > 0) {
        samples.update((xs) => [...xs, ...newSamples].sort((a, b) => a.slot - b.slot));
      }
      if (newProgs.length > 0) {
        programs.update((xs) => [...xs, ...newProgs].sort((a, b) => a.slot - b.slot));
        selectedSlot.set(newProgs[0].slot);
        selectedKeygroupN.set(1);
      }
      const skipped = (res.skipped ?? []).length;
      // Keep this short — it renders in the topbar status chip.
      setSync('dirty',
        `Loaded ${nS} samples, ${nP} programs` + (skipped ? ` (${skipped} skipped)` : ''));
    } catch (e: any) {
      setSync('error', 'Open image failed: ' + String(e?.message ?? e));
    } finally {
      importBusy = false;
    }
  }

  // Export Gotek image: snapshot ALL current programs + every
  // sample that has host audio into a bootable 800 KB floppy image.
  // Mirrors the device's own "save entire memory" mental model —
  // pick-and-choose can come later if disks start overflowing.
  // Samples without host audio (device rows never copied/imported)
  // are skipped client-side; the backend treats them as an error to
  // keep its contract strict.
  let exportBusy = false;
  async function exportGotekImage() {
    if (exportBusy) return;
    exportBusy = true;
    try {
      const progJSONs = get(programs).map((p) => programToJSON(p));
      const sampleRows = get(samples).filter((s) => s.words12 && s.words12.length > 0);
      if (progJSONs.length === 0 && sampleRows.length === 0) {
        setSync('error', 'Nothing to export — load or import programs/samples first');
        return;
      }
      const req = {
        programs: progJSONs,
        samples: sampleRows.map((s) => ({
          params: sampleToSampleParams(s),
          words: s.words12,
        })),
        forceRS232: true,
      };
      const res = await (App as any).ExportGotekImage(req);
      if (!res) return; // user cancelled the dialog
      const skipped = get(samples).length - sampleRows.length;
      // Keep this short — it renders in the topbar status chip.
      setSync('synced',
        `Exported ${res.files} files, ${res.freeBlocks} KB free` +
        (skipped > 0 ? ` (${skipped} skipped)` : ''));
    } catch (e: any) {
      setSync('error', 'Export failed: ' + String(e?.message ?? e));
    } finally {
      exportBusy = false;
    }
  }

  // Page-level shortcuts, matching the statusbar hints (which were
  // decorative mockup text until now): ⌘S saves the patch image,
  // ⌘↵ opens the send preflight. Suppressed while a modal is up so
  // ⌘↵ can't stack a second flow on top of an open one.
  function onPageKeydown(e: KeyboardEvent) {
    if (!(e.metaKey || e.ctrlKey)) return;
    if (modalKind !== 'closed') return;
    if (e.key === 's' || e.key === 'S') {
      e.preventDefault();
      void exportGotekImage();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      openSend();
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
    // The copy exists only in the app until explicitly Sent — even
    // when duplicating a device program, the new slot is local.
    dup.source = 'local';
    programs.update((xs) => [...xs, dup].sort((a, b) => a.slot - b.slot));
    selectedSlot.set(slot);
    selectedKeygroupN.set(1);
    setSync('dirty', 'Unsaved');
  }
</script>

<svelte:window on:keydown={onPageKeydown} />

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
            on:click={() => { selectedSlot.set(p.slot); onProgramSelected(p.slot); }}
            on:keydown={(e) => { if (e.key === 'Enter') { selectedSlot.set(p.slot); onProgramSelected(p.slot); } }}
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
        open a patch (.img) to load programs<br/>or click + to create
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
          Connect to your S950 to load its program catalog, open a
          patch (.img), or start a new program from scratch.
        </p>
        <button type="button" class="btn btn--primary" on:click={newProgram}>
          New program
        </button>
        <button type="button" class="btn" on:click={openGotekImage} disabled={importBusy}>
          {importBusy ? 'Opening…' : 'Open patch (.img)...'}
        </button>
      </div>
    {:else}
    <section class="card card--form top-form">
      <div class="card__head">
        <div class="card__title">Program</div>
        <div class="card__subtitle">
          slot {padSlot(prog.slot)} · {prog.keygroups.length} keygroups
        </div>
        <!-- Follow-selection feedback. Action language only: a green
             "sent PC n" states what we DID; the S950 can't confirm
             what it's playing, so we never claim "synced". Blocked
             reasons land here, next to the MIDI prog # / Respond to
             PC fields that usually cause (and fix) them. -->
        {#if $lastActivation?.slot === prog.slot}
          <div
            class="follow-note follow-note--ok"
            title="Selecting this program sent MIDI Program Change {$lastActivation.midiProg} (channel {$lastActivation.channel + 1}) to the S950. The hardware can't confirm the switch — your ears do.">
            → sent PC {$lastActivation.midiProg}
          </div>
        {:else if $followEnabled && $followBlockedReason}
          <div class="follow-note follow-note--warn" title={$followBlockedReason}>
            ⚠ follow: {$followBlockedReason}
          </div>
        {/if}
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
        <!-- Raw wire value 0..127, matching the PC data byte the
             device compares against (hardware-verified: programs
             holding 0 respond to C0 00). The old 1..128 range made
             real device programs at #0 unrepresentable. -->
        <NumField
          value={prog.midiProg}
          min={0} max={127}
          format={(v) => v.toString().padStart(3, '0')}
          on:change={(e) => {
            selectedProgram.update({ midiProg: e.detail });
            invalidateActivation(prog.slot);
          }} />
      </div>
      <div class="row">
        <span class="row__label">Respond to PC</span>
        <button
          type="button"
          class="toggle {prog.respondPC ? 'on' : ''}"
          on:click={() => {
            selectedProgram.update({ respondPC: !prog.respondPC });
            invalidateActivation(prog.slot);
          }}>
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
      <!-- Two deliberate rows instead of one wrapping flex line:
           device + program lifecycle on top, file I/O below. Keeps
           the groups aligned at any width instead of wrapping
           mid-row with dangling separators. -->
      <div class="actions actions--rows">
        <div class="actions__row">
          <div class="actions__group">
            <button type="button" class="btn" on:click={getFromDevice}>Get from S950</button>
            <button type="button" class="btn btn--primary" on:click={openSend}>Send to S950</button>
          </div>
          <div class="actions__sep"></div>
          <div class="actions__group">
            <button type="button" class="btn" on:click={newProgram}>New program</button>
            <button type="button" class="btn" on:click={duplicateProgram} disabled={!hasProgram}>Duplicate</button>
          </div>
        </div>
        <div class="actions__row">
          <div class="actions__group">
            <button
              type="button"
              class="btn"
              on:click={openGotekImage}
              disabled={importBusy}
              title="Load a patch — an S950 floppy image's programs + samples — into the app as local entries. Works with any Gotek/FlashFloppy or Translator-built .img.">
              {importBusy ? 'Opening…' : 'Open patch (.img)...'}
            </button>
            <button
              type="button"
              class="btn"
              on:click={exportGotekImage}
              disabled={exportBusy}
              title="Save everything — all programs, keygroups, and samples with host audio — as a bootable S950 floppy image. Works in a Gotek/FlashFloppy drive standalone; the image's settings file boots the sampler with controller-select on RS-232C.">
              {exportBusy ? 'Saving…' : 'Save patch (.img)...'}
            </button>
          </div>
        </div>
      </div>
    </section>
    {/if}
  </main>

  <Statusbar
    hints={[
      { key: '↵',  label: 'edit name' },
      { key: '⌘S', label: 'save patch' },
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
        {#if sendPlan}
          <ul class="preflight">
            {#each sendPlan.items as item}
              <li class="preflight__item preflight__item--{item.kind === 'error' ? 'error' : item.kind}">
                <span class="preflight__icon">{item.kind === 'ok' ? '✓' : item.kind === 'warn' ? '⚠' : '✕'}</span>
                <div class="preflight__body">
                  <span class="preflight__title">{item.title}</span>
                  <span class="preflight__detail">{item.detail}</span>
                </div>
              </li>
            {/each}
          </ul>
          <div class="preflight__estimate">
            {#if sendPlan.uploads.length > 0}
              Estimated transfer: {sendPlan.uploads.length} sample{sendPlan.uploads.length === 1 ? '' : 's'}
              + program · ~{Math.floor(sendPlan.estSeconds / 60)}m {String(sendPlan.estSeconds % 60).padStart(2, '0')}s
            {:else}
              Estimated transfer: program only · a few seconds
            {/if}
          </div>
        {/if}
      </div>
      <footer class="modal__foot">
        <button type="button" class="btn" on:click={cancel}>Cancel</button>
        <button
          type="button"
          class="btn btn--primary"
          disabled={!sendPlan?.ok}
          on:click={continueSend}>Continue ▶</button>
      </footer>
    </div>
  </div>
{:else if modalKind === 'transfer'}
  <div class="modal-backdrop is-open" role="dialog" aria-modal="true">
    <div class="modal">
      <header class="modal__head">
        <h2 class="modal__title">
          {#if sendPhase === 'done'}Sent <strong>{prog.name}</strong> to S950
          {:else if sendPhase === 'error'}Send failed
          {:else}Sending <strong>{prog.name}</strong> to S950
          {/if}
        </h2>
        <div class="modal__route">{connectionLabel}</div>
      </header>
      <div class="modal__body">
        <div class="log">
          {#each sendSteps as step}
            <div class="log__row {step.status === 'ok' ? 'ok' : step.status === 'run' ? 'run' : step.status === 'fail' ? 'fail' : 'pending'}">
              {step.status === 'ok' ? '✓' : step.status === 'run' ? '▶' : step.status === 'fail' ? '✕' : '·'}
              {step.label}
            </div>
          {/each}
        </div>
        {#if sendPhase === 'done'}
          <div class="modal__step">✓ Everything landed — these slots now mirror the device.</div>
        {:else if sendPhase === 'error'}
          <div class="modal__step modal__step--error">{sendError || 'Unknown error'}</div>
        {/if}
      </div>
      <footer class="modal__foot">
        {#if sendPhase === 'sending'}
          <span class="modal__waitnote">do not close · in progress</span>
        {:else}
          <button type="button" class="btn btn--primary" on:click={cancel}>Close</button>
        {/if}
      </footer>
    </div>
  </div>
{/if}

<style>
  /* Page-specific only — shared shell + cards + form + modals come
     from shared.css. */
  /* Follow-selection feedback in the identity card head. Matches
     .card__subtitle's mono/uppercase look; pushed to the right edge
     so it reads as status, not part of the slot/keygroup line. */
  .follow-note {
    margin-left: auto;
    font-family: var(--font-mono);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    cursor: help;
  }
  .follow-note--ok   { color: var(--rb-green); }
  .follow-note--warn { color: var(--rb-orange); }
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
  /* Stacked layout: each .actions__row is one aligned line of
     groups. Replaces a single wrapping line whose breaks landed
     mid-group with dangling separators. */
  .actions--rows {
    flex-direction: column;
    align-items: stretch;
  }
  .actions__row {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .actions__group { display: inline-flex; gap: 6px; }
  .actions__sep {
    width: 1px;
    height: 24px;
    background: var(--grey-medium);
    margin: 0 4px;
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
