<script lang="ts">
  import Topbar from '../lib/Topbar.svelte';
  import Statusbar from '../lib/Statusbar.svelte';
  import NumField from '../lib/NumField.svelte';
  import { setSync } from '../lib/sync';
  import { samples, selectedSampleSlot, selectedSample } from '../lib/state/samples';
  import {
    slicing,
    updateSlicing,
    setSliceMode,
    setBeats,
    selectSlice,
    updateSliceAt,
    addSliceAt,
    removeSliceAt,
    slicesFor,
    MAX_SLICES,
    type SliceMode,
    type SliceLoopMode,
    type Division,
  } from '../lib/state/slicing';
  import { onMount } from 'svelte';

  onMount(() => setSync('synced', 'Synced'));

  // ---------- Modal flow ----------
  type ModalKind = 'closed' | 'transfer';
  let modalKind: ModalKind = 'closed';
  function openTransfer() { modalKind = 'transfer'; }
  function cancel()       { modalKind = 'closed'; }

  $: smp = $selectedSample;

  // ---------- Zoom + visible window ----------
  // zoom × zoomCenter define which slice of [0..smp.length] words
  // is currently visible. Marker / region positions and the SVG
  // viewBox are derived from these so everything shares one mapping.
  $: visibleN = (() => {
    const v = smp.length / $slicing.zoom;
    return Math.min(smp.length, Math.max(1, v));
  })();
  $: viewStart = (() => {
    const center = smp.length * $slicing.zoomCenter;
    let s = center - visibleN / 2;
    if (s < 0) s = 0;
    if (s + visibleN > smp.length) s = smp.length - visibleN;
    return s;
  })();
  $: viewEnd   = viewStart + visibleN;
  $: viewBoxX  = (viewStart / smp.length) * 1000;
  $: viewBoxW  = (visibleN / smp.length) * 1000;

  // Word → visible-% mapping. Off-screen markers return out-of-range
  // values; CSS overflow: hidden on the waveform clips them so we
  // don't need to filter in the template.
  function pct(word: number): number {
    if (visibleN <= 0) return 0;
    return ((word - viewStart) / visibleN) * 100;
  }

  // Marker / region positions in visible-% space.
  $: pctStart    = pct(smp.start);
  $: pctEnd      = pct(smp.end);
  $: pctLoopStart = pct(smp.loopStart);
  $: pctLoopWidth = (smp.loopLength / visibleN) * 100;

  $: durationSec = smp.length / smp.rate;

  function onNameInput(e: Event) {
    selectedSample.update({ name: (e.target as HTMLInputElement).value });
  }

  // ---------- Marker drag on the waveform ----------
  // Each marker grabs onto a CSS variable on .waveform. Drag converts
  // pixel x within the strip to a word index (length-relative), then
  // writes to the store with appropriate clamping.
  type MarkerKind = 'start' | 'end' | 'loopStart' | 'loopEnd';
  let waveformEl: HTMLDivElement | undefined;
  let activeMarker: MarkerKind | null = null;

  function beginMarkerDrag(e: MouseEvent, kind: MarkerKind) {
    if (e.button !== 0) return;
    activeMarker = kind;
    e.preventDefault();
    e.stopPropagation();
  }
  function onMarkerMove(e: MouseEvent) {
    if (!activeMarker || !waveformEl) return;
    const r = waveformEl.getBoundingClientRect();
    const ratio = Math.max(0, Math.min(1, (e.clientX - r.left) / r.width));
    // Convert ratio to a word index through the current visible window,
    // so dragging a marker while zoomed in nudges by sub-word amounts.
    const word = Math.round(viewStart + ratio * visibleN);
    if (activeMarker === 'start') {
      selectedSample.update({ start: Math.min(word, smp.end) });
    } else if (activeMarker === 'end') {
      selectedSample.update({ end: Math.max(word, smp.start) });
    } else if (activeMarker === 'loopStart') {
      // Move loopStart but keep loop end where it was if possible.
      const oldEnd = smp.loopStart + smp.loopLength;
      const newStart = Math.max(0, Math.min(word, smp.length));
      selectedSample.update({
        loopStart: newStart,
        loopLength: Math.max(0, oldEnd - newStart),
      });
    } else if (activeMarker === 'loopEnd') {
      const newLength = Math.max(0, Math.min(word, smp.length) - smp.loopStart);
      selectedSample.update({ loopLength: newLength });
    }
  }
  function onMarkerUp() { activeMarker = null; }

  // ---------- Slice preview (stub) ----------
  // Real impl will play just the [start..end] window of the sample
  // through Web Audio (or a Wails-side player). For now it's a
  // console nudge so the UI affordance is wired end-to-end.
  function previewSlice(idx: number) {
    const s = $slicing.slices[idx];
    if (!s) return;
    selectSlice(idx);
    // TODO: wire to backend audio preview.
    console.log(`[preview slice ${idx + 1}] start=${s.start} length=${s.length} mode=${s.loopMode}`);
  }

  // ---------- Manual slice placement ----------
  // In Manual mode, clicking on empty waveform inserts a new slice
  // at the click position. Clicks on existing slice markers are
  // captured by their own handler (stopPropagation) so they select
  // instead of adding a duplicate.
  function onWaveformClick(e: MouseEvent) {
    if (!$slicing.active) return;
    if ($slicing.mode !== 'manual') return;
    if (!waveformEl) return;
    if ($slicing.slices.length >= MAX_SLICES) return;
    // Ignore clicks that originated from a slice drag, whether the
    // event target is the marker itself or .waveform (the case where
    // mouseup landed outside the marker after a drag past a neighbor).
    const target = e.target as HTMLElement | null;
    if (target && target.closest('.slice-marker')) return;
    if (performance.now() - lastDragEnd < 250) return;
    const r = waveformEl.getBoundingClientRect();
    const ratio = Math.max(0, Math.min(1, (e.clientX - r.left) / r.width));
    const word = Math.round(viewStart + ratio * visibleN);
    // TODO: when snapToZero is on, snap `word` to nearest zero crossing.
    addSliceAt(word);
  }

  // ---------- Slice marker drag ----------
  // Mousedown on a slice head: select it and start tracking. Drag
  // converts horizontal motion to word delta through the visible
  // window, so dragging is precise even when zoomed in. Movement is
  // clamped between neighbor slice starts so order stays intact.
  type SliceDrag = { idx: number; startX: number; startWord: number; moved: boolean };
  let sliceDrag: SliceDrag | null = null;
  // Timestamp of the most recent drag release. The synthesised click
  // event that follows a drag-release fires within ~16ms; treat any
  // waveform click inside this window as the tail of the drag and
  // ignore it. More robust than microtask flags, which can fire
  // between mouseup and click depending on the browser.
  let lastDragEnd = 0;

  function onSliceMarkerDown(e: MouseEvent, idx: number) {
    if (e.button !== 0) return;
    selectSlice(idx);
    sliceDrag = {
      idx,
      startX: e.clientX,
      startWord: $slicing.slices[idx].start,
      moved: false,
    };
    e.preventDefault();
    e.stopPropagation();
  }
  function onSliceDragMove(e: MouseEvent) {
    if (!sliceDrag || !waveformEl) return;
    const r = waveformEl.getBoundingClientRect();
    const wordsPerPx = visibleN / r.width;
    const dx = e.clientX - sliceDrag.startX;
    if (Math.abs(dx) > 1) sliceDrag.moved = true;
    let next = Math.round(sliceDrag.startWord + dx * wordsPerPx);
    next = Math.max(0, Math.min(smp.length, next));

    // Update the dragged slice's start, then re-sort the array so
    // numbering follows position. The dragged slice tracks to its new
    // index (slice 9 dragged past 8 becomes the new slice 8).
    const slices = $slicing.slices;
    const oldIdx = sliceDrag.idx;
    const tagged = slices.map((s, i) => ({
      slice: i === oldIdx ? { ...s, start: next } : s,
      wasDragged: i === oldIdx,
    }));
    tagged.sort((a, b) => a.slice.start - b.slice.start);
    const newSlices = tagged.map((t) => t.slice);
    const newIdx = tagged.findIndex((t) => t.wasDragged);

    // Recompute each slice's length from the next start so the
    // adjacent slice shrinks/grows naturally.
    for (let i = 0; i < newSlices.length; i++) {
      const end = i < newSlices.length - 1 ? newSlices[i + 1].start : smp.length;
      newSlices[i].length = Math.max(1, end - newSlices[i].start);
    }
    // Keep the in-flight drag tracking the slice's new position so
    // continuing to move the mouse keeps dragging the same slice.
    sliceDrag.idx = newIdx;
    updateSlicing({ slices: newSlices, selectedIndex: newIdx });
  }
  function onSliceDragUp() {
    if (!sliceDrag) return;
    const wasMoved = sliceDrag.moved;
    sliceDrag = null;
    if (wasMoved) lastDragEnd = performance.now();
  }

  // ---------- Hover cursor + wheel zoom ----------
  // cursorRatio is the mouse's horizontal position (0..1) over the
  // waveform. Wheel zoom anchors at this position so the word under
  // the cursor stays put as zoom changes — same affordance the
  // mockup hinted at with "scroll to zoom".
  let cursorRatio: number | null = null;
  $: cursorWord = cursorRatio !== null
    ? Math.round(viewStart + cursorRatio * visibleN)
    : null;

  function onWaveformMouseMove(e: MouseEvent) {
    if (!waveformEl) return;
    const r = waveformEl.getBoundingClientRect();
    cursorRatio = Math.max(0, Math.min(1, (e.clientX - r.left) / r.width));
  }
  function onWaveformMouseLeave() {
    cursorRatio = null;
  }

  function onWaveformWheel(e: WheelEvent) {
    if (!$slicing.active) return;
    e.preventDefault();
    const factor = e.deltaY < 0 ? 1.25 : 1 / 1.25;
    const newZoom = Math.max(1, Math.min(40, $slicing.zoom * factor));
    if (Math.abs(newZoom - $slicing.zoom) < 0.001) return;
    // Anchor under the cursor; fall back to center if cursor unknown.
    const r = cursorRatio !== null ? cursorRatio : 0.5;
    const oldVisible = smp.length / $slicing.zoom;
    const oldStart = smp.length * $slicing.zoomCenter - oldVisible / 2;
    const cursorWordAt = oldStart + r * oldVisible;
    const newVisible = smp.length / newZoom;
    const newStart = cursorWordAt - r * newVisible;
    const newCenter = (newStart + newVisible / 2) / smp.length;
    updateSlicing({
      zoom: newZoom,
      zoomCenter: Math.max(0, Math.min(1, newCenter)),
    });
  }

  // Delete the selected slice when the user presses Delete/Backspace
  // while slicing is active. Ignored if focus is on an editable input
  // so the user can still type negatives etc. in NumFields.
  function onWindowKeyDown(e: KeyboardEvent) {
    if (!$slicing.active) return;
    if (e.key !== 'Delete' && e.key !== 'Backspace') return;
    const t = e.target as HTMLElement | null;
    if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return;
    if ($slicing.selectedIndex < 0) return;
    e.preventDefault();
    removeSliceAt($slicing.selectedIndex);
  }

  // Typed constants — kept out of templates because Svelte 3's parser
  // doesn't accept `as Foo` casts in attribute expressions.
  const SLICE_MODES: SliceMode[] = ['manual', 'auto', 'beats'];
  const DIVISIONS: Division[] = [4, 8, 16, 32];
  const LOOP_MODES: SliceLoopMode[] = ['one-shot', 'loop', 'ping-pong'];
  function modeLabel(m: SliceMode): string {
    return m[0].toUpperCase() + m.slice(1);
  }
  function loopLabel(m: SliceLoopMode): string {
    return m === 'one-shot' ? 'Once' : m === 'loop' ? 'Loop' : 'Ping-pong';
  }

  // ---------- Waveform path generation ----------
  // Seeded LCG so the path is stable across re-renders for the same
  // sample — re-generated per slot change so different samples look
  // different. Filled top + bottom give the classic mirrored shape.
  let wfTop = '';
  let wfBot = '';
  function regenWaveform(slot: number) {
    let seed = (0x9E37 ^ slot * 0x1ABC) >>> 0;
    const rnd = () => {
      seed = (seed * 1103515245 + 12345) & 0x7fffffff;
      return seed / 0x7fffffff;
    };
    const N = 280;
    const top: string[] = ['M 0,100'];
    const bot: string[] = ['M 0,100'];
    for (let i = 1; i <= N; i++) {
      const t = i / N;
      let env = 92 * Math.exp(-3.2 * t);
      if (t < 0.04) env *= (1 + (0.04 - t) * 8);
      const jit = 0.35 + 0.65 * rnd();
      const amp = env * jit;
      const x = (i / N) * 1000;
      top.push(`L ${x.toFixed(1)},${(100 - amp).toFixed(1)}`);
      bot.push(`L ${x.toFixed(1)},${(100 + amp).toFixed(1)}`);
    }
    top.push('L 1000,100 Z');
    bot.push('L 1000,100 Z');
    wfTop = top.join(' ');
    wfBot = bot.join(' ');
  }
  // Regenerate when the selected sample changes.
  $: regenWaveform($selectedSampleSlot);

  // Ruler ticks every ~70ms across the waveform.
  $: ruler = (() => {
    const out: Array<{ left: number; label: string }> = [];
    const step = 0.07;
    for (let t = 0; t < durationSec; t += step) {
      out.push({ left: (t / durationSec) * 100, label: `${t.toFixed(2)}s` });
    }
    return out;
  })();
</script>

<svelte:window
  on:mousemove={(e) => { onMarkerMove(e); onSliceDragMove(e); }}
  on:mouseup={() => { onMarkerUp(); onSliceDragUp(); }}
  on:keydown={onWindowKeyDown} />

<div class="app app--3row">
  <Topbar slotChip={`${$samples.length} / 100 samples`} />

  <aside class="sidebar">
    <div class="sidebar__head">
      <div class="sidebar__title">Samples</div>
      <div class="sidebar__count">{$samples.length} / 100</div>
    </div>
    <div class="sample-list">
      {#each $samples as s (s.slot)}
        <div
          class="sample {s.slot === $selectedSampleSlot ? 'selected' : ''}"
          on:click={() => selectedSampleSlot.set(s.slot)}
          on:keydown={(e) => e.key === 'Enter' && selectedSampleSlot.set(s.slot)}
          role="button"
          tabindex="0">
          <span class="sample__slot">{s.slot.toString().padStart(2, '0')}</span>
          <span class="sample__name">{s.name}</span>
          <span class="sample__rate">{Math.round(s.rate / 1000)}k</span>
        </div>
      {/each}
    </div>
    <div class="dropzone-hint">
      drag .wav / .aiff files here<br/>to replace this sample
    </div>
  </aside>

  <main class="main">
    <!-- Identity strip -->
    <section class="card identity-card">
      <div class="card__head">
        <div class="card__title">Sample</div>
        <div class="card__subtitle">slot {smp.slot.toString().padStart(2, '0')} · SPRM block synced</div>
      </div>
      <div class="identity">
        <div class="identity__cell">
          <label>Name</label>
          <span class="field field--wide field--yellow">
            <input type="text" value={smp.name} on:input={onNameInput} maxlength="10" />
          </span>
        </div>
        <div class="identity__cell">
          <label>Slot</label>
          <span class="field">{smp.slot.toString().padStart(2, '0')}</span>
        </div>
        <div class="identity__cell">
          <label>Rate</label>
          <span class="field">{(smp.rate / 1000).toFixed(2)} kHz</span>
        </div>
        <div class="identity__cell">
          <label>Length</label>
          <span class="field">{smp.length.toLocaleString()} words</span>
        </div>
        <div class="identity__cell">
          <label>Nominal pitch</label>
          <span class="field">C3 (960)</span>
        </div>
      </div>
    </section>

    <!-- Waveform card -->
    <section class="card waveform-card">
      <div class="card__head">
        <div>
          <div class="card__title">Waveform</div>
          <div class="card__subtitle">
            {#if $slicing.active}
              slice mode · click slice to select · drag head to move
            {:else}
              drag markers to set start / end / loop · scroll to zoom
            {/if}
          </div>
        </div>
        <div class="slice-tools">
          <button
            type="button"
            class="toggle {$slicing.active ? 'on' : ''}"
            on:click={() => updateSlicing({ active: !$slicing.active })}>
            {$slicing.active ? '● Slice' : '○ Slice'}
          </button>
          {#if $slicing.active}
            <div class="slice-tools__group">
              <span class="slice-tools__label">by</span>
              <span class="seg">
                {#each SLICE_MODES as m}
                  <button type="button" class={$slicing.mode === m ? 'on' : ''} on:click={() => setSliceMode(m)}>
                    {modeLabel(m)}
                  </button>
                {/each}
              </span>
            </div>
            <div class="slice-tools__group">
              <span class="slice-tools__label">snap</span>
              <span class="seg">
                <button type="button" class={$slicing.snapToZero ? 'on' : ''} on:click={() => updateSlicing({ snapToZero: true })}>Zero ✓</button>
                <button type="button" class={!$slicing.snapToZero ? 'on' : ''} on:click={() => updateSlicing({ snapToZero: false })}>Off</button>
              </span>
            </div>
            <div class="slice-tools__group">
              <span class="slice-tools__label">zoom</span>
              <span class="seg">
                <button type="button" class={$slicing.zoom === 1 ? 'on' : ''}  on:click={() => updateSlicing({ zoom: 1 })}>1×</button>
                <button type="button" class={$slicing.zoom === 2 ? 'on' : ''}  on:click={() => updateSlicing({ zoom: 2 })}>2×</button>
                <button type="button" class={$slicing.zoom === 4 ? 'on' : ''}  on:click={() => updateSlicing({ zoom: 4 })}>4×</button>
                <button type="button" class={$slicing.zoom === 8 ? 'on' : ''}  on:click={() => updateSlicing({ zoom: 8 })}>8×</button>
              </span>
            </div>
          {/if}
        </div>
      </div>

      {#if $slicing.active && $slicing.mode === 'beats'}
        <!-- Beats sub-picker — appears only in Beats mode. Tempo × bars
             × division → total slice count, capped at MAX_SLICES. -->
        <div class="slice-beats">
          <div class="slice-tools__group">
            <span class="slice-beats__label">tempo</span>
            <NumField
              value={$slicing.tempo}
              min={20} max={300}
              format={(v) => `${v} BPM`}
              on:change={(e) => updateSlicing({ tempo: e.detail })} />
          </div>
          <div class="slice-tools__group">
            <span class="slice-beats__label">bars</span>
            <NumField
              value={$slicing.bars}
              min={1} max={16}
              on:change={(e) => setBeats({ bars: e.detail })} />
          </div>
          <div class="slice-tools__group">
            <span class="slice-beats__label">division</span>
            <span class="seg">
              {#each DIVISIONS as div}
                <button type="button" class={$slicing.division === div ? 'on' : ''} on:click={() => setBeats({ division: div })}>1/{div}</button>
              {/each}
            </span>
          </div>
          <span class="slice-beats__count">→ {slicesFor($slicing.bars, $slicing.division)} slices</span>
        </div>
      {/if}
      <div
        class="waveform"
        class:waveform--slicing={$slicing.active}
        class:waveform--manual={$slicing.active && $slicing.mode === 'manual'}
        bind:this={waveformEl}
        on:click={onWaveformClick}
        on:mousemove={onWaveformMouseMove}
        on:mouseleave={onWaveformMouseLeave}
        on:wheel|preventDefault|nonpassive={onWaveformWheel}
        style="--start: {pctStart}%; --end: {pctEnd}%; --loop-start: {pctLoopStart}%; --loop-width: {pctLoopWidth}%;">
        <svg class="waveform__svg" viewBox="{viewBoxX} 0 {viewBoxW} 200" preserveAspectRatio="none" aria-hidden="true">
          <path d={wfTop} fill="#FDF000" />
          <path d={wfBot} fill="#FDF000" />
        </svg>

        <div class="waveform__overlay">
          <div class="waveform__inactive waveform__inactive--pre"></div>
          <div class="waveform__inactive waveform__inactive--post"></div>
          <div class="waveform__loop"></div>
          <!-- Markers — each has pointer-events: auto so they capture
               their own drag even though the overlay container blocks
               clicks otherwise. -->
          <div class="marker marker--start" on:mousedown={(e) => beginMarkerDrag(e, 'start')}>
            <span class="marker__handle" on:mousedown={(e) => beginMarkerDrag(e, 'start')}>Start · {smp.start.toLocaleString()}</span>
          </div>
          <div class="marker marker--end" on:mousedown={(e) => beginMarkerDrag(e, 'end')}>
            <span class="marker__handle marker__handle--right" on:mousedown={(e) => beginMarkerDrag(e, 'end')}>End · {smp.end.toLocaleString()}</span>
          </div>
          <div class="marker marker--loop-start" on:mousedown={(e) => beginMarkerDrag(e, 'loopStart')}>
            <span class="marker__handle" on:mousedown={(e) => beginMarkerDrag(e, 'loopStart')}>Loop · {smp.loopStart.toLocaleString()}</span>
          </div>
          <div class="marker marker--loop-end" on:mousedown={(e) => beginMarkerDrag(e, 'loopEnd')}>
            <span class="marker__handle marker__handle--right" on:mousedown={(e) => beginMarkerDrag(e, 'loopEnd')}>{(smp.loopStart + smp.loopLength).toLocaleString()}</span>
          </div>

          {#if $slicing.active}
            {#each $slicing.slices as sl, i (i)}
              <div
                class="slice-marker {i === $slicing.selectedIndex ? 'is-selected' : ''}"
                style="--x: {pct(sl.start)}%;"
                on:mousedown={(e) => onSliceMarkerDown(e, i)}
                on:keydown={(e) => e.key === 'Enter' && selectSlice(i)}
                role="button"
                tabindex="0">
                <span class="slice-marker__head">{String(i + 1).padStart(2, '0')}</span>
                <span class="slice-marker__line"></span>
              </div>
            {/each}

            <!-- Hover cursor. Shows where the next click would drop a
                 slice (Manual mode) and is the anchor for wheel zoom.
                 Hidden when the mouse isn't over the waveform. -->
            {#if cursorRatio !== null}
              <div class="waveform__cursor" style="left: {cursorRatio * 100}%;">
                <span class="waveform__cursor__badge">
                  {cursorWord?.toLocaleString()}{$slicing.zoom > 1 ? ` · ${Math.round($slicing.zoom * 100)}%` : ''}
                </span>
              </div>
            {/if}
          {/if}

          <div class="waveform__ruler">
            {#each ruler as r}
              <span style="left: {r.left}%">{r.label}</span>
            {/each}
          </div>
        </div>
      </div>
      <div class="waveform__legend">
        {#if $slicing.active}
          <span><span class="legend-swatch" style="--swatch: var(--rb-yellow)"></span>Slice</span>
          <span><span class="legend-swatch" style="--swatch: var(--rb-magenta)"></span>Selected</span>
        {/if}
        <span><span class="legend-swatch" style="--swatch: var(--rb-green)"></span>Start</span>
        <span><span class="legend-swatch" style="--swatch: var(--rb-red)"></span>End</span>
        <span style="margin-left:auto;">
          {#if $slicing.active}{$slicing.slices.length} / {MAX_SLICES} slices · {/if}{(smp.rate / 1000).toFixed(2)} kHz · {smp.length.toLocaleString()} words · ~{durationSec.toFixed(2)} s
        </span>
      </div>
    </section>

    <!-- Three columns: markers/slices / playback / device.
         The first column swaps between Markers (no slicing) and
         Slices (slicing active). -->
    <section class="controls">
      {#if !$slicing.active}
        <div class="card">
          <div class="card__head">
            <div class="card__title">Markers</div>
            <div class="card__subtitle">in sample words</div>
          </div>
          <div class="row">
            <label>Start</label>
            <NumField
              value={smp.start}
              min={0} max={smp.end}
              format={(v) => v.toLocaleString()}
              on:change={(e) => selectedSample.update({ start: e.detail })} />
          </div>
          <div class="row">
            <label>End</label>
            <NumField
              value={smp.end}
              min={smp.start} max={smp.length}
              format={(v) => v.toLocaleString()}
              on:change={(e) => selectedSample.update({ end: e.detail })} />
          </div>
          <div class="row">
            <label>Loop start</label>
            <NumField
              value={smp.loopStart}
              min={0} max={smp.length}
              format={(v) => v.toLocaleString()}
              on:change={(e) => selectedSample.update({ loopStart: e.detail })} />
          </div>
          <div class="row">
            <label>Loop length</label>
            <NumField
              value={smp.loopLength}
              min={0} max={smp.length}
              format={(v) => v.toLocaleString()}
              on:change={(e) => selectedSample.update({ loopLength: e.detail })} />
          </div>
        </div>
      {:else}
        {@const sel = $slicing.slices[$slicing.selectedIndex]}
        <div class="card">
          <div class="card__head">
            <div class="card__title">Slices</div>
            <div class="card__subtitle">{$slicing.slices.length} / {MAX_SLICES}</div>
          </div>
          <div class="slice-list">
            {#each $slicing.slices as sl, i (i)}
              <div
                class="slice-list__row {i === $slicing.selectedIndex ? 'is-selected' : ''}"
                on:click={() => selectSlice(i)}
                on:keydown={(e) => e.key === 'Enter' && selectSlice(i)}
                role="button"
                tabindex="0">
                <span class="slice-list__num">{String(i + 1).padStart(2, '0')}</span>
                <span>{sl.start.toLocaleString()} — {(sl.start + sl.length).toLocaleString()}</span>
                <span class="slice-list__loop">{sl.loopMode}</span>
                <button
                  type="button"
                  class="slice-list__play"
                  aria-label={`Preview slice ${i + 1}`}
                  on:click|stopPropagation={() => previewSlice(i)}>▶</button>
              </div>
            {/each}
          </div>
          {#if sel}
            <div class="row">
              <label>Slice</label>
              <NumField
                value={$slicing.selectedIndex + 1}
                min={1} max={$slicing.slices.length}
                format={(v) => `${String(v).padStart(2, '0')} of ${$slicing.slices.length}`}
                on:change={(e) => selectSlice(e.detail - 1)} />
            </div>
            <div class="row">
              <label>Start</label>
              <NumField
                value={sel.start}
                min={0} max={smp.length}
                format={(v) => v.toLocaleString()}
                on:change={(e) => updateSliceAt($slicing.selectedIndex, { start: e.detail })} />
            </div>
            <div class="row">
              <label>Length</label>
              <NumField
                value={sel.length}
                min={1} max={smp.length}
                format={(v) => v.toLocaleString()}
                on:change={(e) => updateSliceAt($slicing.selectedIndex, { length: e.detail })} />
            </div>
            <div class="row row--inline">
              <label>Loop</label>
              <div class="mode-row">
                {#each LOOP_MODES as m}
                  <button type="button" class="toggle {sel.loopMode === m ? 'on' : ''}"
                    on:click={() => updateSliceAt($slicing.selectedIndex, { loopMode: m })}>
                    {loopLabel(m)}
                  </button>
                {/each}
              </div>
            </div>
            <div class="row">
              <label>Loop start</label>
              <NumField
                value={sel.loopStart}
                min={0} max={sel.length}
                format={(v) => v.toLocaleString()}
                on:change={(e) => updateSliceAt($slicing.selectedIndex, { loopStart: e.detail })} />
            </div>
            <div class="row">
              <label>Loop length</label>
              <NumField
                value={sel.loopLength}
                min={0} max={sel.length}
                format={(v) => v.toLocaleString()}
                on:change={(e) => updateSliceAt($slicing.selectedIndex, { loopLength: e.detail })} />
            </div>
          {/if}
        </div>
      {/if}

      <div class="card">
        <div class="card__head">
          <div class="card__title">Playback</div>
          <div class="card__subtitle">SPRM replay flags</div>
        </div>
        <!-- Mode lives in a flex row instead of the 120-px label grid
             so all three toggles fit at narrow column widths. -->
        <div class="row row--inline">
          <label>Mode</label>
          <div class="mode-row">
            <button type="button" class="toggle {smp.mode === 'one-shot' ? 'on' : ''}" on:click={() => selectedSample.update({ mode: 'one-shot' })}>Once</button>
            <button type="button" class="toggle {smp.mode === 'loop' ? 'on' : ''}"     on:click={() => selectedSample.update({ mode: 'loop' })}>Loop</button>
            <button type="button" class="toggle {smp.mode === 'alt' ? 'on' : ''}"      on:click={() => selectedSample.update({ mode: 'alt' })}>Alt</button>
          </div>
        </div>
        <div class="row">
          <label>Reverse</label>
          <button type="button" class="toggle {smp.reverse ? 'on' : ''}" on:click={() => selectedSample.update({ reverse: !smp.reverse })}>
            {smp.reverse ? 'On' : 'Off'}
          </button>
        </div>
        <div class="row">
          <label>Vel x-fade</label>
          <button type="button" class="toggle {smp.velXfade ? 'on' : ''}" on:click={() => selectedSample.update({ velXfade: !smp.velXfade })}>
            {smp.velXfade ? 'On' : 'Off'}
          </button>
        </div>

        <hr class="panel__divider" />

        <div class="row">
          <label>Tune</label>
          <NumField
            value={smp.tune}
            min={-50} max={50} step={0.01}
            format={(v) => `${v >= 0 ? '+' : ''}${v.toFixed(2)} st`}
            on:change={(e) => selectedSample.update({ tune: e.detail })} />
        </div>
        <div class="row">
          <label>Loudness</label>
          <NumField
            value={smp.loudness}
            on:change={(e) => selectedSample.update({ loudness: e.detail })} />
        </div>
      </div>

      <div class="card">
        <div class="card__head">
          <div class="card__title">Device &amp; file</div>
          <div class="card__subtitle">MRCC Port 03 · slot {smp.slot.toString().padStart(2, '0')}</div>
        </div>
        <div class="actions actions--stack">
          {#if $slicing.active}
            <!-- Headline action when slicing: uploads N samples to the
                 next free slots and auto-builds a program with N
                 keygroups mapped chromatically. The pre-flight modal
                 (designed earlier) gates the multi-sample transfer. -->
            <button type="button" class="btn btn--primary" on:click={openTransfer}>
              Apply slicing → {$slicing.slices.length} samples + program
            </button>
            <hr class="panel__divider" />
          {/if}
          <button type="button" class="btn btn--primary" on:click={openTransfer}>Import .wav / .aiff…</button>
          <button type="button" class="btn" on:click={openTransfer}>Replace from S950</button>
          <button type="button" class="btn" on:click={openTransfer}>Download .wav…</button>
          <hr class="panel__divider" />
          <button type="button" class="btn">Send SPRM to S950</button>
          <button type="button" class="btn">Get SPRM from S950</button>
        </div>
      </div>
    </section>
  </main>

  <Statusbar
    hints={[
      { key: 'drag', label: 'marker to nudge' },
      { key: 'scroll', label: 'zoom waveform' },
      { key: 'space', label: 'preview' },
      { key: '⌘↵', label: 'send to s950' },
    ]}
    status={`${smp.name} · slot ${smp.slot.toString().padStart(2, '0')} · ${smp.mode} · ${smp.length.toLocaleString()} words`}
  />
</div>

{#if modalKind === 'transfer'}
  <div class="modal-backdrop is-open" role="dialog" aria-modal="true">
    <div class="modal">
      <header class="modal__head">
        <h2 class="modal__title">Uploading <strong>{smp.name}</strong> to S950</h2>
        <div class="modal__route">MRCC Port 03 · Ch 0 · slot {smp.slot.toString().padStart(2, '0')}</div>
      </header>
      <div class="modal__body">
        <div class="modal__step">
          Sample data · block 104 of 207
          <strong>{smp.name} → slot {smp.slot.toString().padStart(2, '0')} ({Math.floor(smp.length / 2).toLocaleString()} / {smp.length.toLocaleString()} words)</strong>
        </div>
        <div class="progress">
          <div class="progress__bar" style="width: 50%;"></div>
          <div class="progress__label">50%</div>
        </div>
        <div class="modal__times">
          <span>Elapsed 0m 32s</span>
          <span>~0m 32s remaining</span>
        </div>
        <div class="log">
          <div class="log__row ok">✓ Connected to S950 on MRCC Port 03</div>
          <div class="log__row warn">⚠ Slot {smp.slot.toString().padStart(2, '0')} occupied — overwriting</div>
          <div class="log__row ok">✓ Sample header accepted (ACK)</div>
          <div class="log__row run">▶ Sending blocks 1–207 · ACK 103/207</div>
          <div class="log__row pending">· SPRM update pending</div>
        </div>
      </div>
      <footer class="modal__foot">
        <button type="button" class="btn" on:click={cancel}>Cancel transfer</button>
      </footer>
    </div>
  </div>
{/if}

<style>
  /* Flex column fills the grid area exactly. Identity is fixed at
     the top, controls fixed at the bottom, the waveform card flexes
     to whatever space is left — so on short windows the waveform
     shrinks instead of the bottom controls falling off-screen. */
  .main {
    grid-area: main;
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 16px;
    background: var(--grey-light);
    height: 100%;
    min-height: 0;
    overflow: hidden;
  }
  .identity-card { flex: 0 0 auto; }
  .waveform-card {
    flex: 1 1 0;
    min-height: 200px;
    display: flex;
    flex-direction: column;
  }
  .waveform-card :global(.waveform) {
    flex: 1 1 0;
    height: auto;
    min-height: 0;
  }

  .identity {
    display: grid;
    grid-template-columns: 2fr 1fr 1fr 1fr 1fr;
    gap: 14px;
    align-items: center;
  }
  .identity__cell { display: flex; flex-direction: column; gap: 3px; }
  .identity__cell > label {
    font-family: var(--font-mono);
    text-transform: uppercase;
    font-size: 9px;
    letter-spacing: 0.08em;
    color: var(--grey-dark);
  }
  .identity__cell > :global(.field) { padding: 4px 8px; }

  /* ---------- Waveform display ----------
     Height clamps so the waveform shrinks on short viewports
     instead of pushing the controls below the fold. */
  .waveform {
    position: relative;
    height: clamp(160px, 32vh, 280px);
    background: var(--canvas);
    border: var(--bw) solid var(--black);
    border-radius: var(--r);
    overflow: hidden;
  }
  .waveform__svg { display: block; width: 100%; height: 100%; }
  .waveform::before {
    content: "";
    position: absolute;
    left: 0; right: 0; top: 50%;
    border-top: 1px dashed rgba(255,255,255,0.12);
    pointer-events: none;
  }
  .waveform__overlay {
    position: absolute;
    inset: 0;
    pointer-events: none;
  }
  .waveform__loop {
    position: absolute;
    top: 0; bottom: 18px;
    left: var(--loop-start);
    width: var(--loop-width);
    background: color-mix(in srgb, var(--rb-cyan) 14%, transparent);
    border-left: 1px solid var(--rb-cyan);
    border-right: 1px solid var(--rb-cyan);
  }
  .waveform__inactive {
    position: absolute;
    top: 0; bottom: 18px;
    background: rgba(0,0,0,0.55);
  }
  .waveform__inactive--pre  { left: 0; width: var(--start); }
  .waveform__inactive--post { right: 0; width: calc(100% - var(--end)); }

  .marker {
    position: absolute;
    top: 0; bottom: 18px;
    width: 2px;
    margin-left: -1px;
    background: var(--marker-color, var(--rb-green));
    pointer-events: auto;
    cursor: ew-resize;
  }
  .marker--start      { left: var(--start);                                    --marker-color: var(--rb-green); }
  .marker--end        { left: var(--end);                                      --marker-color: var(--rb-red); }
  .marker--loop-start { left: var(--loop-start);                                --marker-color: var(--rb-cyan); }
  .marker--loop-end   { left: calc(var(--loop-start) + var(--loop-width));      --marker-color: var(--rb-cyan); }
  .marker__handle {
    position: absolute;
    top: 0;
    background: var(--marker-color);
    color: var(--black);
    font-family: var(--font-mono);
    font-size: 9px;
    padding: 1px 5px;
    border-radius: 0 0 3px 3px;
    transform: translateX(-50%);
    left: 1px;
    white-space: nowrap;
    pointer-events: auto;
    cursor: ew-resize;
  }
  .marker__handle--right { transform: translateX(calc(-100% + 1px)); }

  .waveform__ruler {
    position: absolute;
    left: 0; right: 0; bottom: 0;
    height: 18px;
    background: rgba(0,0,0,0.55);
    border-top: 1px solid var(--canvas-grid-strong);
    font-family: var(--font-mono);
    font-size: 9px;
    color: var(--grey-medium);
  }
  .waveform__ruler :global(span) {
    position: absolute;
    top: 4px;
    transform: translateX(-50%);
  }
  .waveform__legend {
    display: flex;
    flex-wrap: wrap;
    gap: 14px;
    margin-top: 10px;
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--grey-dark);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .waveform__legend :global(span) { display: inline-flex; align-items: center; gap: 6px; }
  .legend-swatch {
    display: inline-block;
    width: 12px; height: 3px;
    background: var(--swatch);
    border-radius: 1px;
  }

  /* Three-column grid for the cards below the waveform. Fixed at
     the bottom of .main; each card scrolls internally if its rows
     exceed the allotted height so nothing falls off the viewport. */
  .controls {
    display: grid;
    grid-template-columns: 1fr 1fr 1fr;
    gap: 16px;
    flex: 0 0 auto;
    min-height: 0;
  }
  .controls :global(.card) {
    max-height: 280px;
    overflow-y: auto;
  }
  .actions--stack {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .actions--stack :global(.btn) { text-align: left; }

  /* ---------- Slice mode ----------
     Mirrors mockups/sample.html. The slice toolbar sits on the right
     of the waveform card head; the beats sub-picker appears below
     only in Beats mode. */
  .slice-tools {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: flex-end;
    gap: 8px 10px;
    font-family: var(--font-mono);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--grey-dark);
    min-width: 0;
  }
  .slice-tools :global(.seg) { font-size: 10px; flex-wrap: nowrap; }
  .slice-tools :global(.seg button) { padding: 3px 7px; white-space: nowrap; }
  .slice-tools__group {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    flex-wrap: nowrap;
  }
  .slice-tools__label { color: var(--grey-dark); white-space: nowrap; }

  .slice-beats {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 12px;
    margin: 8px 0 4px;
    padding: 8px 10px;
    background: var(--grey-light);
    border-radius: var(--r);
    font-family: var(--font-mono);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--grey-dark);
  }
  .slice-beats__label { color: var(--grey-dark); }
  .slice-beats__count {
    margin-left: auto;
    color: var(--black);
    font-weight: 700;
  }

  /* Slice markers on the waveform — yellow numbered tab + thin
     vertical guide. Selected slice flips to magenta. */
  .slice-marker {
    position: absolute;
    top: 0; bottom: 18px;
    left: var(--x);
    width: 0;
    pointer-events: auto;
    z-index: 2;
  }
  .slice-marker__head {
    position: absolute;
    top: 2px;
    left: 0;
    transform: translateX(-50%);
    background: var(--rb-yellow);
    color: var(--black);
    font-family: var(--font-mono);
    font-size: 9px;
    font-weight: 700;
    padding: 1px 4px;
    border: 1px solid var(--black);
    border-radius: 3px;
    cursor: grab;
  }
  .slice-marker__line {
    position: absolute;
    top: 18px; bottom: 0;
    left: 0;
    width: 1px;
    background: rgba(255, 255, 255, 0.35);
    transform: translateX(-50%);
    pointer-events: none;
  }
  .slice-marker:hover .slice-marker__line { background: rgba(255, 255, 255, 0.6); }
  .slice-marker.is-selected .slice-marker__head {
    background: var(--rb-magenta);
    color: var(--white);
    border-color: var(--white);
  }
  .slice-marker.is-selected .slice-marker__line {
    background: var(--rb-magenta);
    width: 2px;
    margin-left: -0.5px;
  }

  /* When slice mode is active, dim the start/end/loop overlay so
     slice markers visually dominate without removing the existing
     UI entirely. */
  .waveform--slicing :global(.marker),
  .waveform--slicing :global(.waveform__loop),
  .waveform--slicing :global(.waveform__inactive) {
    opacity: 0.25;
    pointer-events: none;
  }

  /* Cursor hint for Manual mode — click to drop a new slice. */
  .waveform--manual { cursor: crosshair; }

  /* Hover playhead — tracks the mouse over the waveform when slicing
     is active. Wheel-zoom anchors at this position; the badge shows
     the word at the cursor (and zoom level if not 100%). */
  .waveform__cursor {
    position: absolute;
    top: 0;
    bottom: 18px;
    width: 1px;
    pointer-events: none;
    z-index: 1;
    background-image: linear-gradient(
      to bottom,
      rgba(255,255,255,0.55) 0,
      rgba(255,255,255,0.55) 3px,
      transparent 3px,
      transparent 6px
    );
    background-size: 100% 6px;
  }
  .waveform__cursor__badge {
    position: absolute;
    left: 3px;
    bottom: 4px;
    background: var(--rb-cyan);
    color: var(--black);
    font-family: var(--font-mono);
    font-size: 9px;
    padding: 1px 5px;
    border-radius: 3px;
    border: 1px solid var(--black);
    white-space: nowrap;
  }

  /* Slice list inside the Slices card — scrollable up to MAX_SLICES.
     Each row: num / range / loop mode / play. */
  .slice-list {
    margin: -4px 0 8px;
    max-height: 120px;
    overflow-y: auto;
    border: 1px solid var(--grey-light);
    border-radius: var(--r);
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .slice-list__row {
    display: grid;
    grid-template-columns: 28px 1fr auto 22px;
    align-items: center;
    gap: 6px;
    padding: 3px 8px;
    cursor: pointer;
  }
  .slice-list__row + .slice-list__row { border-top: 1px solid var(--grey-light); }
  .slice-list__row:hover { background: var(--grey-light); }
  .slice-list__row.is-selected { background: var(--rb-yellow); }
  .slice-list__row.is-selected .slice-list__loop { color: var(--black); }
  .slice-list__num { font-weight: 700; color: var(--grey-dark); }
  .slice-list__loop { color: var(--grey-dark); font-size: 10px; }
  .slice-list__play {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 20px; height: 20px;
    border: 1px solid var(--black);
    border-radius: 3px;
    background: var(--white);
    color: var(--black);
    font-size: 10px;
    cursor: pointer;
    padding: 0;
  }
  .slice-list__play:hover { background: var(--rb-yellow); }

  /* Inline row variant — label width is content-sized instead of a
     fixed 120px grid track, so the toggles get the full remaining
     width of the card. */
  .row--inline {
    display: flex;
    grid-template-columns: unset;
    align-items: center;
    gap: 8px;
    padding: 4px 0;
  }
  .mode-row {
    display: inline-flex;
    gap: 4px;
    flex-wrap: nowrap;
    min-width: 0;
  }
  .mode-row :global(.toggle) {
    padding: 3px 8px;
    flex-shrink: 0;
  }
</style>
