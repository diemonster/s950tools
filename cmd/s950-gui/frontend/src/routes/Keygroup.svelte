<script lang="ts">
  import Topbar from '../lib/Topbar.svelte';
  import Statusbar from '../lib/Statusbar.svelte';
  import NumField from '../lib/NumField.svelte';
  import Combobox from '../lib/Combobox.svelte';
  import AdsrEditor from '../lib/AdsrEditor.svelte';
  import { setSync } from '../lib/sync';
  import {
    selectedProgram,
    selectedKeygroupN,
    selectedKeygroup,
    newKeygroup,
    isAssignedSample,
    MAX_KEYGROUPS,
    type Keygroup,
    type Program,
  } from '../lib/state/programs';
  import { navigate } from '../lib/route';
  import {
    samples,
    selectedSampleSlot,
  } from '../lib/state/samples';
  import { rangeLabel, noteName, midiX, midiW, midiBandClipped } from '../lib/midi';
  import { onMount } from 'svelte';

  // Keygroup tab live-syncs (~400ms debounce in real impl). Stub
  // status here is always "Synced" since edits are local.
  onMount(() => setSync('synced', 'Synced'));

  // ---------- Splitter drag ----------
  // Same behavior as the mockup: drag the 6px bar to shrink/grow the
  // properties row. Reads/writes --props-h on the .app element.
  // Match the CSS default in shared.css (.app--canvas grid row). Was
  // 400px; bumped so the LFO / Filter routing / Velocity routing
  // panel fits without scrolling on a standard window height.
  let propsH = 480;
  let dragging = false;
  let startY = 0;
  let startH = 0;
  const MIN_PROPS = 140;
  const MIN_CANVAS = 220;

  function onSplitDown(e: MouseEvent) {
    dragging = true;
    startY = e.clientY;
    startH = propsH;
    e.preventDefault();
  }
  function onSplitMove(e: MouseEvent) {
    if (!dragging) return;
    const dy = startY - e.clientY;
    const max = window.innerHeight - 56 - 28 - 6 - MIN_CANVAS;
    propsH = Math.max(MIN_PROPS, Math.min(max, startH + dy));
  }
  function onSplitUp() {
    dragging = false;
  }

  // Empty-state sentinels. The Keygroup tab depends on the user
  // having loaded/created a program first. We render a CTA when
  // that's not the case; these defaults keep reactive derivations
  // type-safe while the CTA is shown.
  const EMPTY_PROGRAM: Program = {
    slot: -1, name: '', midiProg: 1, respondPC: false,
    keyTilt: 0, positionalXfade: false, keygroups: [],
  };
  const EMPTY_KEYGROUP: Keygroup = newKeygroup(1);

  // Convenience accessors so templates stay readable. The reactive
  // derived stores keep these in sync as the user clicks around.
  $: kg = $selectedKeygroup ?? EMPTY_KEYGROUP;
  $: prog = $selectedProgram ?? EMPTY_PROGRAM;
  $: hasKeygroup = !!$selectedKeygroup;

  // Options for the Soft/Loud sample combobox. Sourced from the
  // shared samples store so adding samples elsewhere reflects here.
  $: sampleOptions = $samples.map((s) => ({
    label: s.name,
    value: s.name,
    meta: s.slot.toString().padStart(2, '0'),
  }));

  // ---------- Drag/drop sample → zone ----------
  // The sidebar lists samples; dragging one onto a zone sets that
  // zone's soft sample. Bind to loud by holding shift on drop.
  const DT_TYPE = 'text/x-s950-sample';
  function onSampleDragStart(e: DragEvent, name: string) {
    if (!e.dataTransfer) return;
    e.dataTransfer.setData(DT_TYPE, name);
    e.dataTransfer.effectAllowed = 'copy';
  }
  function onZoneDragOver(e: DragEvent) {
    if (e.dataTransfer?.types.includes(DT_TYPE)) {
      e.preventDefault();
      e.dataTransfer.dropEffect = 'copy';
    }
  }
  function onZoneDrop(e: DragEvent, kgN: number) {
    const name = e.dataTransfer?.getData(DT_TYPE);
    if (!name) return;
    e.preventDefault();
    selectedKeygroupN.set(kgN);
    // Shift = drop into loud layer; default = soft.
    const target = e.shiftKey ? 'loud' : 'soft';
    if (target === 'loud') {
      selectedKeygroup.updateLoud({ sample: name });
    } else {
      selectedKeygroup.updateSoft({ sample: name });
    }
  }

  // ---------- Zone drag (move / resize / vel) ----------
  // Body drag transposes the whole zone, edge drags resize one bound,
  // top-edge drag adjusts the velocity switch. The grid's pixel size
  // gives us px-per-MIDI-key and px-per-velocity-unit conversions.
  type DragMode = 'move' | 'lo' | 'hi' | 'vel' | null;
  let gridEl: HTMLDivElement | undefined;
  let zoneDrag: {
    mode: DragMode;
    kgN: number;
    startX: number;
    startY: number;
    startLow: number;
    startHigh: number;
    startVel: number;
  } | null = null;

  function beginZoneDrag(e: MouseEvent, k: Keygroup, mode: DragMode) {
    if (e.button !== 0) return;
    selectedKeygroupN.set(k.n);
    zoneDrag = {
      mode,
      kgN: k.n,
      startX: e.clientX,
      startY: e.clientY,
      startLow: k.lowKey,
      startHigh: k.highKey,
      startVel: k.vel,
    };
    e.preventDefault();
    e.stopPropagation();
  }
  function onZoneDragMove(e: MouseEvent) {
    if (!zoneDrag || !gridEl) return;
    const r = gridEl.getBoundingClientRect();
    const pxPerKey = r.width / 88;
    const pxPerVel = r.height / 128;
    const dxKeys = Math.round((e.clientX - zoneDrag.startX) / pxPerKey);
    const dyVel  = Math.round((zoneDrag.startY - e.clientY) / pxPerVel); // up = higher vel

    const lo0 = zoneDrag.startLow, hi0 = zoneDrag.startHigh, v0 = zoneDrag.startVel;
    const clampK = (v: number) => Math.max(0, Math.min(127, v));
    const clampV = (v: number) => Math.max(1, Math.min(128, v));

    if (zoneDrag.mode === 'move') {
      // Keep range width, shift both bounds.
      let nlo = clampK(lo0 + dxKeys);
      let nhi = clampK(hi0 + dxKeys);
      const width = hi0 - lo0;
      // Re-anchor so width is preserved even if we hit a wall.
      if (nlo === 0 && lo0 + dxKeys < 0) nhi = width;
      if (nhi === 127 && hi0 + dxKeys > 127) nlo = 127 - width;
      selectedKeygroup.update({ lowKey: nlo, highKey: nhi });
    } else if (zoneDrag.mode === 'lo') {
      const nlo = Math.min(hi0, clampK(lo0 + dxKeys));
      selectedKeygroup.update({ lowKey: nlo });
    } else if (zoneDrag.mode === 'hi') {
      const nhi = Math.max(lo0, clampK(hi0 + dxKeys));
      selectedKeygroup.update({ highKey: nhi });
    } else if (zoneDrag.mode === 'vel') {
      selectedKeygroup.update({ vel: clampV(v0 + dyVel) });
    }
  }
  function onZoneDragUp() {
    zoneDrag = null;
  }

  // ---------- Properties row splitter ----------
  // Drag the horizontal divider between Row 1 (per-zone) and Row 2
  // (modulation) so values that overflow either row can be brought
  // into view. Tracked in pixels; clamped to keep both rows usable.
  let propsEl: HTMLElement | undefined;
  let row1H = '1fr';
  let rowDrag: { startY: number; startRow1: number } | null = null;

  function onRowSplitDown(e: MouseEvent) {
    if (!propsEl) return;
    const firstPanel = propsEl.children[0] as HTMLElement | undefined;
    if (!firstPanel) return;
    rowDrag = { startY: e.clientY, startRow1: firstPanel.offsetHeight };
    e.preventDefault();
  }
  function onRowSplitMove(e: MouseEvent) {
    if (!rowDrag || !propsEl) return;
    const dy = e.clientY - rowDrag.startY;
    const totalH = propsEl.offsetHeight;
    const max = totalH - 80 - 6; // 80 = min row 2, 6 = splitter
    const next = Math.max(80, Math.min(max, rowDrag.startRow1 + dy));
    row1H = `${next}px`;
  }
  function onRowSplitUp() { rowDrag = null; }

  // Zone style — absolute % position on the canvas grid for one keygroup.
  // Clipped to the visible A0..C8 strip: a keygroup whose range
  // extends past the rendered keyboard (e.g. factory TONE spans
  // F#-1..D#8 = MIDI 18..123) would otherwise spill the band across
  // the y-axis label columns. Returns null when the keygroup's range
  // sits entirely outside the strip — caller should skip rendering.
  function zoneStyle(k: Keygroup): string | null {
    const band = midiBandClipped(k.lowKey, k.highKey);
    if (!band) return null;
    const yTop = 100 - (k.vel * 100) / 128;
    return `--x: ${band.left.toFixed(2)}%; --w: ${band.width.toFixed(2)}%; --y-top: ${yTop.toFixed(1)}%; --y-bottom: 0%; --zone-bg: var(${k.color});`;
  }

  // Octave markers under the grid.
  const octMarkers = [
    { midi: 24, label: 'C0' },
    { midi: 36, label: 'C2' },
    { midi: 48, label: 'C3' },
    { midi: 60, label: 'C4' },
    { midi: 72, label: 'C5' },
    { midi: 84, label: 'C6' },
    { midi: 96, label: 'C7' },
  ];

  // 88-key sharps under the keyboard — generated from the white-key
  // pattern (positions 1, 3, 6, 8, 10 in each 12-key octave have a
  // black key to their left/right).
  const sharps = (() => {
    const out: number[] = [];
    for (let k = 21; k <= 108; k++) {
      const pc = k % 12;
      if (pc === 1 || pc === 3 || pc === 6 || pc === 8 || pc === 10) {
        out.push(midiX(k) + 50 / 88); // center on the key seam
      }
    }
    return out;
  })();
</script>

<svelte:window
  on:mousemove={(e) => { onSplitMove(e); onZoneDragMove(e); onRowSplitMove(e); }}
  on:mouseup={() => { onSplitUp(); onZoneDragUp(); onRowSplitUp(); }} />

<div
  class="app app--canvas"
  class:is-resizing={dragging}
  style="--props-h: {propsH}px;">
  <Topbar slotCount={`${$samples.length} / 100`} slotNoun="samples" />

  <!-- Samples sidebar — drag source for zone binding (drag impl is TODO). -->
  <aside class="sidebar">
    <div class="sidebar__panel">
      <div class="sidebar__head">
        <div class="sidebar__title">Samples</div>
        <div class="sidebar__count">{$samples.length} / 100</div>
      </div>
      <div class="sample-list">
        {#each $samples as smp (smp.slot)}
          <div
            class="sample {smp.slot === $selectedSampleSlot ? 'selected' : ''}"
            draggable="true"
            on:dragstart={(e) => onSampleDragStart(e, smp.name)}
            on:click={() => selectedSampleSlot.set(smp.slot)}
            on:keydown={(e) => e.key === 'Enter' && selectedSampleSlot.set(smp.slot)}
            role="button"
            tabindex="0">
            <span class="sample__slot">{smp.slot.toString().padStart(2, '0')}</span>
            <span class="sample__name">{smp.name}</span>
            <span class="sample__rate">{Math.round(smp.rate / 1000)}k</span>
          </div>
        {/each}
      </div>
      <div class="sidebar__hint">
        drag a sample onto a zone to bind<br/>(shift + drop = loud layer)
      </div>
    </div>
  </aside>

  <!-- 2D zone canvas. When no program/keygroup is loaded (fresh app,
       no Connect, no New) we show a CTA that points the user back to
       the Program tab — the keygroup editor has nothing to operate on
       without a program. The empty-state spans the canvas + splitter
       + properties rows so it gets a full-width panel rather than
       being squeezed into the canvas grid's narrow first cell. -->
  {#if !hasKeygroup}
    <div class="kg-empty">
      <div class="empty-state">
        <div class="empty-state__title">No keygroup selected</div>
        <p class="empty-state__hint">
          A keygroup belongs to a program. Pick or create a program first.
        </p>
        <button type="button" class="btn btn--primary" on:click={() => navigate('program')}>
          Go to Program tab
        </button>
      </div>
    </div>
  {:else}
  <section class="canvas">
    <div class="canvas__head">
      <div class="group">
        <button
          class="head-chip"
          type="button"
          disabled={prog.keygroups.length >= MAX_KEYGROUPS}
          title={prog.keygroups.length >= MAX_KEYGROUPS
            ? `Program is at the S950's ${MAX_KEYGROUPS}-keygroup cap`
            : 'Append a new keygroup spanning the full keyboard'}
          on:click={() => {
            const n = selectedProgram.addKeygroup();
            if (n !== null) selectedKeygroupN.set(n);
          }}>+ Add Zone</button>
      </div>
    </div>

    <div class="y-axis left">
      <span>127</span><span>96</span><span>64</span><span>32</span><span>0</span>
    </div>
    <div class="y-axis right">
      <span>127</span><span>96</span><span>64</span><span>32</span><span>0</span>
    </div>

    <div class="grid" bind:this={gridEl}>
      {#each prog.keygroups as k (k.n)}
        {@const style = zoneStyle(k)}
        {#if style}
          <div
            class="zone {k.n === $selectedKeygroupN ? 'selected' : ''}"
            style={style}
            on:mousedown={(e) => beginZoneDrag(e, k, 'move')}
            on:dragover={onZoneDragOver}
            on:drop={(e) => onZoneDrop(e, k.n)}
            on:keydown={(e) => e.key === 'Enter' && selectedKeygroupN.set(k.n)}
            role="button"
            tabindex="0">
            <!-- Edge handles: left/right resize the key range, top
                 adjusts the velocity switch. stopPropagation in the
                 handler keeps the body's move-drag from also firing. -->
            <span class="zone__handle zone__handle--lo" on:mousedown={(e) => beginZoneDrag(e, k, 'lo')}></span>
            <span class="zone__handle zone__handle--hi" on:mousedown={(e) => beginZoneDrag(e, k, 'hi')}></span>
            <span class="zone__handle zone__handle--vel" on:mousedown={(e) => beginZoneDrag(e, k, 'vel')}></span>
            <span class="zone__label">{k.soft.sample}</span>
            <span class="zone__meta">{rangeLabel(k.lowKey, k.highKey)}</span>
          </div>
        {/if}
      {/each}
    </div>

    <div class="keys-strip">
      {#each octMarkers as m}
        <span style="left: {midiX(m.midi).toFixed(2)}%">{m.label}</span>
      {/each}
    </div>

    <div class="kb-pad"></div>
    <div class="keyboard">
      <div class="keyboard__bg"></div>
      <div class="keyboard__sharps" aria-hidden="true">
        {#each sharps as left}
          <span style="left: {left.toFixed(2)}%"></span>
        {/each}
      </div>
      <!-- Clip the band to A0..C8 (the rendered strip). A keygroup
           whose upper key is past C8 (e.g. the factory TONE program
           ranges to D#8 = MIDI 123) would otherwise project the band
           past the last visible key, since the band's % width is
           computed relative to the 88-key strip. -->
      <div class="keyboard__assigned">
        {#each prog.keygroups as k (k.n)}
          {@const band = midiBandClipped(k.lowKey, k.highKey)}
          {#if band}
            <span style="left: {band.left.toFixed(2)}%; width: {band.width.toFixed(2)}%; --zone-bg: var({k.color});"></span>
          {/if}
        {/each}
      </div>
    </div>
    <div class="kb-pad-r"></div>
  </section>

  <div class="splitter" on:mousedown={onSplitDown} role="separator" aria-orientation="horizontal"></div>

  <!-- Properties panel — Row 1 + Row 2 in a 3-col grid -->
  <section class="properties" bind:this={propsEl} style="--row1-h: {row1H};">
    <!-- Keygroup panel.
         Behavior toggles sit at the top so they're always visible
         without resizing the properties row. Numeric fields below. -->
    <div class="panel">
      <div class="panel__title">Keygroup · selected</div>
      <div class="toggle-row">
        <button type="button" class="toggle {kg.oneShot ? 'on' : ''}" on:click={() => selectedKeygroup.update({ oneShot: !kg.oneShot })}>One-shot</button>
        <button type="button" class="toggle {kg.constPitch ? 'on' : ''}" on:click={() => selectedKeygroup.update({ constPitch: !kg.constPitch })}>Const-pitch</button>
      </div>

      <hr class="panel__divider" />

      <div class="row">
        <span class="row__label">Lower key</span>
        <NumField
          value={kg.lowKey}
          min={0} max={kg.highKey}
          format={(v) => `${noteName(v)} (${v})`}
          on:change={(e) => selectedKeygroup.update({ lowKey: e.detail })} />
      </div>
      <div class="row">
        <span class="row__label">Upper key</span>
        <NumField
          value={kg.highKey}
          min={kg.lowKey} max={127}
          format={(v) => `${noteName(v)} (${v})`}
          on:change={(e) => selectedKeygroup.update({ highKey: e.detail })} />
      </div>
      <div class="row">
        <span class="row__label">Vel switch</span>
        <NumField
          value={kg.vel}
          min={1} max={128}
          on:change={(e) => selectedKeygroup.update({ vel: e.detail })} />
      </div>
      <div class="row" title="Per-keygroup MIDI channel offset (added to the program's basic channel). 0..15.">
        <span class="row__label">MIDI ch. offset</span>
        <NumField
          value={kg.midiChannel}
          min={0} max={15}
          on:change={(e) => selectedKeygroup.update({ midiChannel: e.detail })} />
      </div>
      <!-- Voice out assigns the keygroup to a physical/individual
           output. The S950 supports 'ALL' (mixed L+R), individual
           outputs 1–8, plus 'L' / 'R' for the master stereo bus.
           Backed by a native <select> wrapped in .field so it picks
           up the same chip styling + ▾ chevron as the rest of the
           panel. -->
      <div class="row">
        <span class="row__label">Voice out</span>
        <span class="field field--wide field--select voice-out">
          <span class="field__value">{kg.voiceOut}</span>
          <select
            value={kg.voiceOut}
            on:change={(e) => selectedKeygroup.update({ voiceOut: e.currentTarget.value })}>
            <option value="ALL">ALL</option>
            <option value="1">1</option>
            <option value="2">2</option>
            <option value="3">3</option>
            <option value="4">4</option>
            <option value="5">5</option>
            <option value="6">6</option>
            <option value="7">7</option>
            <option value="8">8</option>
            <option value="L">L</option>
            <option value="R">R</option>
          </select>
        </span>
      </div>
    </div>

    <!-- Soft layer -->
    <div class="panel">
      <div class="panel__title">
        Soft layer <span class="panel__tag">vel &lt; {kg.vel}</span>
      </div>
      <div class="layer-hint">plays at velocities below the switch</div>
      <div class="row">
        <span class="row__label">Sample</span>
        <Combobox
          value={kg.soft.sample}
          options={sampleOptions}
          extra="field--yellow"
          on:change={(e) => selectedKeygroup.updateSoft({ sample: e.detail })} />
      </div>
      <div class="row">
        <span class="row__label">Transpose</span>
        <NumField
          value={kg.soft.transpose}
          min={-50} max={50} step={0.01}
          format={(v) => `${v >= 0 ? '+' : ''}${v.toFixed(2)} st`}
          on:change={(e) => selectedKeygroup.updateSoft({ transpose: e.detail })} />
      </div>
      <div class="row">
        <span class="row__label">Filter</span>
        <NumField
          value={kg.soft.filter}
          min={0} max={99}
          on:change={(e) => selectedKeygroup.updateSoft({ filter: e.detail })} />
      </div>
      <div class="row">
        <span class="row__label">Loudness</span>
        <NumField
          value={kg.soft.loudness}
          on:change={(e) => selectedKeygroup.updateSoft({ loudness: e.detail })} />
      </div>
    </div>

    <!-- Loud layer.
         Akai's firmware initializes the loud sample slot with the
         literal placeholder "2 SAMPLE" on factory programs. We treat
         placeholders as unassigned for display + disabled-state
         purposes, but keep the raw value in kg.loud.sample so we
         re-emit identical bytes if the user doesn't touch the row. -->
    <div class="panel panel--last-col">
      <div class="panel__title">
        Loud layer <span class="panel__tag panel__tag--muted">vel ≥ {kg.vel}</span>
      </div>
      <div class="layer-hint">{isAssignedSample(kg.loud.sample) ? 'plays at velocities at or above the switch' : 'assign a sample to enable this layer'}</div>
      <div class="row">
        <span class="row__label">Sample</span>
        <Combobox
          value={isAssignedSample(kg.loud.sample) ? kg.loud.sample : ''}
          options={sampleOptions}
          on:change={(e) => selectedKeygroup.updateLoud({ sample: e.detail })} />
      </div>
      <div class="row">
        <span class="row__label">Transpose</span>
        <NumField
          value={kg.loud.transpose}
          min={-50} max={50} step={0.01}
          format={(v) => `${v >= 0 ? '+' : ''}${v.toFixed(2)} st`}
          disabled={!isAssignedSample(kg.loud.sample)}
          on:change={(e) => selectedKeygroup.updateLoud({ transpose: e.detail })} />
      </div>
      <div class="row">
        <span class="row__label">Filter</span>
        <NumField
          value={kg.loud.filter}
          min={0} max={99}
          disabled={!isAssignedSample(kg.loud.sample)}
          on:change={(e) => selectedKeygroup.updateLoud({ filter: e.detail })} />
      </div>
      <div class="row">
        <span class="row__label">Loudness</span>
        <NumField
          value={kg.loud.loudness}
          disabled={!isAssignedSample(kg.loud.sample)}
          on:change={(e) => selectedKeygroup.updateLoud({ loudness: e.detail })} />
      </div>
    </div>

    <!-- Row splitter: drag to rebalance vertical space between the
         per-zone row (above) and the modulation row (below). -->
    <div class="row-splitter" on:mousedown={onRowSplitDown} role="separator" aria-orientation="horizontal"></div>

    <!-- Amp envelope. -->
    <div class="panel">
      <div class="panel__title">Amp envelope</div>
      <div class="adsr-strip">
        <div class="adsr-cell">A<NumField value={kg.mod.ampEnv.a} min={0} max={99} extra="field--xs"
          on:change={(e) => selectedKeygroup.updateMod({ ampEnv: { ...kg.mod.ampEnv, a: e.detail } })} /></div>
        <div class="adsr-cell">D<NumField value={kg.mod.ampEnv.d} min={0} max={99} extra="field--xs"
          on:change={(e) => selectedKeygroup.updateMod({ ampEnv: { ...kg.mod.ampEnv, d: e.detail } })} /></div>
        <div class="adsr-cell">S<NumField value={kg.mod.ampEnv.s} min={0} max={99} extra="field--xs"
          on:change={(e) => selectedKeygroup.updateMod({ ampEnv: { ...kg.mod.ampEnv, s: e.detail } })} /></div>
        <div class="adsr-cell">R<NumField value={kg.mod.ampEnv.r} min={0} max={99} extra="field--xs"
          on:change={(e) => selectedKeygroup.updateMod({ ampEnv: { ...kg.mod.ampEnv, r: e.detail } })} /></div>
      </div>
      <AdsrEditor
        a={kg.mod.ampEnv.a} d={kg.mod.ampEnv.d} s={kg.mod.ampEnv.s} r={kg.mod.ampEnv.r}
        stroke="#FDF000"
        on:change={(e) => selectedKeygroup.updateMod({ ampEnv: { ...kg.mod.ampEnv, ...e.detail } })} />
    </div>

    <!-- Filter envelope (same pattern). -->
    <div class="panel">
      <div class="panel__title">Filter envelope</div>
      <div class="adsr-strip">
        <div class="adsr-cell">A<NumField value={kg.mod.filterEnv.a} min={0} max={99} extra="field--xs"
          on:change={(e) => selectedKeygroup.updateMod({ filterEnv: { ...kg.mod.filterEnv, a: e.detail } })} /></div>
        <div class="adsr-cell">D<NumField value={kg.mod.filterEnv.d} min={0} max={99} extra="field--xs"
          on:change={(e) => selectedKeygroup.updateMod({ filterEnv: { ...kg.mod.filterEnv, d: e.detail } })} /></div>
        <div class="adsr-cell">S<NumField value={kg.mod.filterEnv.s} min={0} max={99} extra="field--xs"
          on:change={(e) => selectedKeygroup.updateMod({ filterEnv: { ...kg.mod.filterEnv, s: e.detail } })} /></div>
        <div class="adsr-cell">R<NumField value={kg.mod.filterEnv.r} min={0} max={99} extra="field--xs"
          on:change={(e) => selectedKeygroup.updateMod({ filterEnv: { ...kg.mod.filterEnv, r: e.detail } })} /></div>
      </div>
      <AdsrEditor
        a={kg.mod.filterEnv.a} d={kg.mod.filterEnv.d} s={kg.mod.filterEnv.s} r={kg.mod.filterEnv.r}
        stroke="#4AC9E3"
        on:change={(e) => selectedKeygroup.updateMod({ filterEnv: { ...kg.mod.filterEnv, ...e.detail } })} />
    </div>

    <!-- LFO & dynamics — packs LFO + filter routing + velocity
         routing + pitch warp into a single cell. Uses .panel--mod
         to claw back top/bottom padding + title margin so all four
         subsections fit without scrolling. -->
    <div class="panel panel--last-col panel--mod">
      <div class="panel__title">LFO &amp; dynamics</div>

      <div class="panel__subtitle">LFO</div>
      <!-- Paired rows save vertical space — LFO has 5 knobs, the
           odd one out (Fade in) sits alone on its row. -->
      <div class="grid-2">
        <div class="row row--compact">
          <span class="row__label">Rate</span>
          <NumField value={kg.mod.lfo.rate} min={0} max={99} extra="field--xs"
            on:change={(e) => selectedKeygroup.updateMod({ lfo: { ...kg.mod.lfo, rate: e.detail } })} />
        </div>
        <div class="row row--compact">
          <span class="row__label">Depth</span>
          <NumField value={kg.mod.lfo.depth} min={0} max={99} extra="field--xs"
            on:change={(e) => selectedKeygroup.updateMod({ lfo: { ...kg.mod.lfo, depth: e.detail } })} />
        </div>
        <div class="row row--compact">
          <span class="row__label">Fade in</span>
          <NumField value={kg.mod.lfo.fadeIn} min={0} max={99} extra="field--xs"
            on:change={(e) => selectedKeygroup.updateMod({ lfo: { ...kg.mod.lfo, fadeIn: e.detail } })} />
        </div>
        <div></div>
        <div class="row row--compact">
          <span class="row__label">Mod-wheel</span>
          <NumField value={kg.mod.lfo.modWheel} min={0} max={99} extra="field--xs"
            on:change={(e) => selectedKeygroup.updateMod({ lfo: { ...kg.mod.lfo, modWheel: e.detail } })} />
        </div>
        <div class="row row--compact">
          <span class="row__label">Aftertouch</span>
          <NumField value={kg.mod.lfo.aftertouch} min={0} max={99} extra="field--xs"
            on:change={(e) => selectedKeygroup.updateMod({ lfo: { ...kg.mod.lfo, aftertouch: e.detail } })} />
        </div>
      </div>

      <div class="panel__subtitle">Filter routing</div>
      <div class="grid-2">
        <div class="row row--compact">
          <span class="row__label">Env → VCF</span>
          <NumField value={kg.mod.envToVCF} min={0} max={99} extra="field--xs"
            on:change={(e) => selectedKeygroup.updateMod({ envToVCF: e.detail })} />
        </div>
        <div class="row row--compact">
          <span class="row__label">Key track</span>
          <NumField value={kg.mod.keyTrack} min={0} max={99} extra="field--xs"
            on:change={(e) => selectedKeygroup.updateMod({ keyTrack: e.detail })} />
        </div>
      </div>

      <!-- Velocity routing uses the same .grid-2 → .row--compact
           layout as LFO + Filter routing above so every value box in
           the panel sits in the same column rather than fanning to
           the flex-end edges (which made the rows look misaligned
           when label widths varied). -->
      <div class="panel__subtitle">Velocity routing</div>
      <div class="grid-2">
        <div class="row row--compact">
          <span class="row__label">→ filter</span>
          <NumField value={kg.mod.vel.toFilter} min={0} max={99} extra="field--xs"
            on:change={(e) => selectedKeygroup.updateMod({ vel: { ...kg.mod.vel, toFilter: e.detail } })} />
        </div>
        <div class="row row--compact">
          <span class="row__label">→ loudness</span>
          <NumField value={kg.mod.vel.toLoud} min={0} max={99} extra="field--xs"
            on:change={(e) => selectedKeygroup.updateMod({ vel: { ...kg.mod.vel, toLoud: e.detail } })} />
        </div>
        <div class="row row--compact">
          <span class="row__label">→ attack</span>
          <NumField value={kg.mod.vel.toAttack} min={0} max={99} extra="field--xs"
            on:change={(e) => selectedKeygroup.updateMod({ vel: { ...kg.mod.vel, toAttack: e.detail } })} />
        </div>
        <div class="row row--compact">
          <span class="row__label">→ release</span>
          <NumField value={kg.mod.vel.toRelease} min={0} max={99} extra="field--xs"
            on:change={(e) => selectedKeygroup.updateMod({ vel: { ...kg.mod.vel, toRelease: e.detail } })} />
        </div>
      </div>

      <!-- Pitch warp is a single knob — wrap it in .grid-2 with an
           empty right cell so its value box still aligns under the
           left-column value boxes above. -->
      <div class="panel__subtitle">Pitch warp</div>
      <div class="grid-2">
        <div class="row row--compact">
          <span class="row__label">Amount</span>
          <NumField value={kg.mod.warpAmount} min={0} max={99} extra="field--xs"
            on:change={(e) => selectedKeygroup.updateMod({ warpAmount: e.detail })} />
        </div>
        <div></div>
      </div>
    </div>
  </section>
  {/if}

  <Statusbar
    hints={[
      { key: 'dblclick', label: 'new zone' },
      { key: 'drag', label: 'edges to resize' },
      { key: 'drop', label: 'wav / aiff to add sample' },
    ]}
    status={hasKeygroup
      ? `${prog.keygroups.length} keygroups`
      : 'no keygroup selected'}
  />
</div>

<style>
  /* Page-specific styles — the 2D zone canvas, splitter, properties
     panel + modulation helpers. Everything else is in shared.css. */

  .splitter {
    grid-area: splitter;
    background: var(--black);
    cursor: row-resize;
    position: relative;
    -webkit-user-select: none;
    user-select: none;
  }
  .splitter::after {
    content: "";
    position: absolute;
    left: 50%; top: 50%;
    width: 36px; height: 2px;
    background: var(--grey-medium);
    border-radius: 2px;
    transform: translate(-50%, -50%);
  }
  .splitter:hover::after { background: var(--rb-yellow); }
  .is-resizing { cursor: row-resize; }
  .is-resizing :global(*) {
    -webkit-user-select: none !important;
    user-select: none !important;
  }

  /* Sidebar samples are drag-source on this tab. */
  .sidebar :global(.sample) { cursor: grab; }

  /* ---------- Canvas ---------- */
  /* Empty-state wrapper. The .app--canvas grid has canvas / splitter /
     properties as three separate rows in the right column; when no
     keygroup is selected we want the CTA panel to fill all three so
     it doesn't get cramped into the narrow canvas row. The wrapper
     starts at the canvas row and stretches through the properties
     row, in the second column only (sidebar keeps its area). */
  .kg-empty {
    grid-column: 2 / 3;
    grid-row: 2 / 5;
    padding: 16px;
    background: var(--main-bg);
    display: flex;
  }

  .canvas {
    grid-area: canvas;
    background: var(--canvas);
    color: var(--canvas-text);
    position: relative;
    display: grid;
    grid-template-rows: auto 1fr 28px 56px;
    grid-template-columns: 38px 1fr 38px;
    grid-template-areas:
      "canvas-head canvas-head canvas-head"
      "y-left      grid         y-right"
      ".           keys-strip   ."
      "kb-pad      keyboard     kb-pad-r";
    overflow: hidden;
  }
  .canvas__head {
    grid-area: canvas-head;
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 8px 12px;
    background: #111;
    border-bottom: 1px solid var(--canvas-grid-strong);
  }
  .canvas__head .group { display: flex; gap: 6px; align-items: center; }
  .head-chip {
    font-family: var(--font-mono);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-size: 10px;
    padding: 3px 8px;
    border: 1px solid var(--canvas-grid-strong);
    border-radius: 999px;
    color: var(--canvas-text);
    background: transparent;
    cursor: pointer;
  }

  .y-axis {
    display: flex;
    flex-direction: column;
    justify-content: space-between;
    padding: 4px 6px;
    font-family: var(--font-mono);
    font-size: 9px;
    color: var(--grey-dark);
    text-align: right;
    background: #161616;
  }
  .y-axis.left  { grid-area: y-left; }
  .y-axis.right { grid-area: y-right; text-align: left; }

  .grid {
    grid-area: grid;
    position: relative;
    background:
      repeating-linear-gradient(to right,
        transparent 0 calc(100% / 88 - 1px),
        var(--canvas-grid) calc(100% / 88 - 1px) calc(100% / 88)),
      repeating-linear-gradient(to bottom,
        transparent 0 calc(100% / 8 - 1px),
        var(--canvas-grid) calc(100% / 8 - 1px) calc(100% / 8));
    background-color: var(--canvas);
    border-left: 1px solid var(--canvas-grid-strong);
    border-right: 1px solid var(--canvas-grid-strong);
  }
  .grid::before {
    content: "";
    position: absolute;
    inset: 0;
    pointer-events: none;
    background:
      repeating-linear-gradient(to right,
        transparent 0 calc(100% / 88 * 12 - 1px),
        var(--canvas-grid-strong) calc(100% / 88 * 12 - 1px) calc(100% / 88 * 12));
  }
  .grid::after {
    content: "";
    position: absolute;
    left: 0; right: 0;
    top: 50%;
    border-top: 1px dashed var(--canvas-grid-strong);
    pointer-events: none;
  }

  .keys-strip {
    grid-area: keys-strip;
    position: relative;
    background: #111;
    border-top: 1px solid var(--canvas-grid-strong);
    font-family: var(--font-mono);
    font-size: 9px;
    color: var(--grey-medium);
  }
  .keys-strip :global(span) {
    position: absolute;
    top: 50%;
    transform: translate(-50%, -50%);
  }

  .zone {
    position: absolute;
    left: var(--x);
    width: var(--w);
    top: var(--y-top);
    bottom: var(--y-bottom);
    border: var(--bw) solid var(--zone-bg, var(--rb-yellow));
    background: color-mix(in srgb, var(--zone-bg, var(--rb-yellow)) 22%, transparent);
    border-radius: 4px;
    display: flex;
    flex-direction: column;
    justify-content: flex-end;
    padding: 4px 6px;
    /* Body cursor signals "draggable to transpose." Edge handles
       override with resize cursors via .zone__handle. */
    cursor: grab;
    box-shadow: inset 0 0 0 1px rgba(0,0,0,0.4);
  }
  .zone:active { cursor: grabbing; }

  /* Edge handles for resize. Each is a thin strip overlapping the
     zone border so it's always grabbable, even on a 1-key wide zone.
     Top handle adjusts the velocity-switch line. */
  .zone__handle {
    position: absolute;
    background: transparent;
    z-index: 2;
  }
  .zone__handle--lo  { left: -4px;  top: 0; bottom: 0; width: 8px;  cursor: ew-resize; }
  .zone__handle--hi  { right: -4px; top: 0; bottom: 0; width: 8px;  cursor: ew-resize; }
  .zone__handle--vel { left: 0; right: 0; top: -4px;  height: 8px; cursor: ns-resize; }
  .zone__label {
    font-family: var(--font-mono);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--paper);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .zone__meta {
    font-family: var(--font-mono);
    font-size: 9px;
    color: var(--grey-medium);
  }
  .zone.selected {
    box-shadow:
      inset 0 0 0 1px rgba(0,0,0,0.6),
      0 0 0 2px var(--rb-yellow);
  }

  .keyboard {
    grid-area: keyboard;
    position: relative;
    background: #fafafa;
    border-top: 1px solid var(--canvas-grid-strong);
    overflow: hidden;
  }
  .keyboard__bg {
    position: absolute; inset: 0;
    background:
      repeating-linear-gradient(to right,
        var(--white) 0 calc(100% / 88 - 1px),
        #d0d0d0 calc(100% / 88 - 1px) calc(100% / 88));
  }
  .keyboard__sharps {
    position: absolute;
    top: 0; bottom: 50%;
    left: 0; right: 0;
    pointer-events: none;
  }
  .keyboard__sharps :global(span) {
    position: absolute;
    top: 0; bottom: 0;
    width: calc(100% / 88 * 0.66);
    background: #212121;
    transform: translateX(-50%);
  }
  .kb-pad   { grid-area: kb-pad;   background: var(--canvas); }
  .kb-pad-r { grid-area: kb-pad-r; background: var(--canvas); }
  .keyboard__assigned {
    position: absolute; inset: 0;
    pointer-events: none;
  }
  .keyboard__assigned :global(span) {
    position: absolute;
    top: 50%; bottom: 0;
    background: var(--zone-bg, var(--rb-yellow));
    opacity: 0.5;
  }

  /* ---------- Properties panel ----------
     Two rows separated by a draggable 6px splitter so the user can
     rebalance vertical space between the per-zone row and the
     modulation row. --row1-h is set inline by Svelte during drag. */
  .properties {
    grid-area: properties;
    background: var(--white);
    border-top: var(--bw) solid var(--black);
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) minmax(0, 1fr);
    /* minmax(0, ...) on both rows so panel content cannot expand the
       track past its share — overflow then scrolls inside the panel
       (or, on short viewports, the media-query density kicks in). */
    grid-template-rows: minmax(0, var(--row1-h, 1fr)) 6px minmax(0, 1fr);
    gap: 0;
    overflow: hidden;
    min-height: 0;
  }
  .panel {
    padding: 12px 16px;
    border-right: var(--bw) solid var(--black);
    overflow-y: auto;
    min-height: 0;
    /* Force the panel to size to its grid cell — without this the
       intrinsic min-content can leak past row boundaries. */
    height: 100%;
    max-height: 100%;
    /* macOS hides overlay scrollbars by default; force a visible
       track + thumb so users can tell when content extends past the
       panel bottom (LFO / Filter routing / Velocity routing in the
       modulation column, behavior toggles + numeric rows in the
       Keygroup · selected column). */
    scrollbar-width: thin;
    scrollbar-color: var(--grey-medium) transparent;
  }
  .panel::-webkit-scrollbar { width: 8px; }
  .panel::-webkit-scrollbar-track { background: transparent; }
  .panel::-webkit-scrollbar-thumb {
    background: var(--grey-medium);
    border-radius: 4px;
    border: 2px solid var(--white);
  }
  .panel::-webkit-scrollbar-thumb:hover { background: var(--grey-dark); }

  /* Progressive density — when the window is short, every panel
     shrinks its paddings, fonts, and form-row gaps in step so the
     full content fits without resorting to the splitter or scroll. */
  @media (max-height: 820px) {
    .panel { padding: 10px 12px; }
    .panel__title { font-size: 9px; margin-bottom: 6px; }
    .panel :global(.row) { padding: 2px 0; gap: 6px; font-size: 12px; }
    .panel :global(.row label) { font-size: 9px; }
    .panel :global(.field) { font-size: 11px; padding: 1px 6px; }
    .panel__divider { margin: 8px 0 6px; }
    .layer-hint { font-size: 9px; }
    .toggle-row :global(.toggle) { font-size: 9px; padding: 2px 7px; }
  }
  @media (max-height: 700px) {
    .panel { padding: 8px 10px; }
    .panel__title { font-size: 8px; margin-bottom: 4px; letter-spacing: 0.06em; }
    .panel :global(.row) { padding: 1px 0; font-size: 11px; }
    .panel :global(.field) { font-size: 10px; padding: 0 5px; }
    .panel__subtitle { font-size: 8px; margin: 6px 0 1px; }
    .adsr-strip { gap: 3px; margin: 1px 0 4px; }
    .row--compact { padding: 1px 0; }
    .panel__divider { margin: 6px 0 4px; }
    .toggle-row :global(.toggle) { font-size: 8px; padding: 2px 6px; }
  }

  /* No border on the rightmost column. nth-child can't be used here
     because the row-splitter is a sibling and offsets the count;
     panels in col 3 carry an explicit class. */
  .panel--last-col { border-right: none; }
  /* Tight modulation panel — same tokens as .panel but with reduced
     top/bottom padding and a smaller title margin so the four
     stacked subsections (LFO · Filter routing · Velocity routing ·
     Pitch warp) fit in row 2 without forcing the user to scroll. */
  .panel--mod { padding: 6px 14px 8px; }
  .panel--mod .panel__title { margin-bottom: 2px; }

  /* Row splitter — same look as the canvas/properties splitter
     above. Spans all 3 columns explicitly. */
  /* Hairline splitter — single 1px rule centered in the 6px hit
     zone, plus the small grab indicator. Subtle but visible. */
  .row-splitter {
    grid-row: 2;
    grid-column: 1 / -1;
    background: transparent;
    cursor: row-resize;
    position: relative;
    -webkit-user-select: none;
    user-select: none;
  }
  .row-splitter::before {
    content: "";
    position: absolute;
    left: 0; right: 0; top: 50%;
    height: 1px;
    background: var(--black);
    transform: translateY(-50%);
  }
  .row-splitter::after {
    content: "";
    position: absolute;
    left: 50%; top: 50%;
    width: 36px; height: 2px;
    background: var(--grey-medium);
    border-radius: 2px;
    transform: translate(-50%, -50%);
    opacity: 0.55;
  }
  .row-splitter:hover::after {
    background: var(--rb-yellow);
    opacity: 1;
  }
  .panel__title {
    font-family: var(--font-mono);
    text-transform: uppercase;
    letter-spacing: 0.1em;
    font-size: 10px;
    color: var(--grey-dark);
    margin-bottom: 8px;
  }
  /* Subtle "(KICK)" sample tag next to layer headings. */
  .panel__title .panel__tag {
    color: var(--ink);
    background: var(--rb-yellow);
    border: 1px solid var(--black);
    border-radius: 3px;
    padding: 0 5px;
    margin-left: 6px;
    letter-spacing: 0.04em;
    font-size: 10px;
  }
  .panel__title .panel__tag--muted {
    color: var(--grey-dark);
    background: var(--grey-light);
    border-color: var(--grey-medium);
  }
  .layer-hint {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--grey-dark);
    margin: -4px 0 8px 0;
    letter-spacing: 0.04em;
  }
  .panel__divider {
    margin: 10px 0 6px 0;
    border: none;
    border-top: 1px solid var(--grey-light);
  }
  .properties :global(.row) { grid-template-columns: 110px 1fr; padding: 3px 0; }
  /* The Keygroup · selected column has 3 toggles + divider + 5 rows,
     more vertical content than the Soft/Loud layer panels next to it
     — without this tightening the bottom row (Voice out) clips into
     the row splitter at the default --props-h. */
  .properties .panel:first-of-type :global(.row) { padding: 2px 0; }

  .toggle-row {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-top: 4px;
  }

  /* Compact strip — see mockups/keygroup.html.
     minmax(0, 1fr) on the field track (not plain 1fr) so the
     NumField's intrinsic min-content can't push the cell past its
     share — without this, the field's stepper buttons hold the
     track open and the next column's content gets overlapped. */
  .row--compact { grid-template-columns: 84px minmax(0, 1fr); padding: 1px 0; gap: 6px; font-size: 11px; }
  .row--compact :global(.row__label) { font-size: 10px; }
  .adsr-strip {
    display: grid;
    grid-template-columns: 1fr 1fr 1fr 1fr;
    gap: 4px;
    margin: 2px 0 8px;
  }
  .adsr-cell {
    display: flex;
    align-items: baseline;
    gap: 2px;
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--grey-dark);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    /* Allow the cell to shrink without overflow when the panel
       is narrow — the inner .field will absorb the squeeze. */
    min-width: 0;
  }
  .adsr-cell :global(.field) {
    flex: 1;
    padding: 1px 2px;
    font-size: 11px;
    justify-content: center;
    text-align: center;
    min-width: 0;
  }
  /* Subsection labels inside LFO & dynamics. Tight margins because
     the panel packs LFO + filter routing + velocity routing + pitch
     warp into one row-2 cell — every saved pixel keeps the bottom
     row (Pitch warp · Amount) visible without scrolling. */
  .panel__subtitle {
    font-family: var(--font-mono);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-size: 9px;
    color: var(--grey-dark);
    margin: 6px 0 1px;
  }
  .panel__subtitle:first-of-type { margin-top: 0; }
  /* Compact field variant for the tightly-packed modulation panels.
     Smaller padding + smaller font. NumField applies field--xs to its
     internal .field via the extra prop, so the selector must be
     :global() — the class never lands on a DOM node this stylesheet
     directly owns. */
  :global(.field--xs) { padding: 0 4px; font-size: 10px; min-width: 0; line-height: 1.25; }

  /* 2-column grid for paired compact rows (LFO, Filter routing,
     Velocity routing, Pitch warp).
     The inner .row already lays out label + field; here we override
     the label width to ~68px so the field has room in the narrow
     half-column. */
  .grid-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0 8px;
    min-width: 0;
  }
  .grid-2 :global(.row--compact) {
    grid-template-columns: 68px minmax(0, 1fr);
    gap: 4px;
    min-width: 0;
  }

  /* Below ~1100px the three properties columns get too narrow for
     the label+field pairs. Stack to a single column and let the
     panels flow naturally. Placed at the very end of the stylesheet
     so these rules override every earlier .panel / .row-splitter
     declaration regardless of where in the file they appear. */
</style>
