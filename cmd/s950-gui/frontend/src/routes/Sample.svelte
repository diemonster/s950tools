<script lang="ts">
  import Topbar from '../lib/Topbar.svelte';
  import Statusbar from '../lib/Statusbar.svelte';
  import NumField from '../lib/NumField.svelte';
  import { setSync } from '../lib/sync';
  import {
    samples, selectedSampleSlot, selectedSample,
    newLocalSample, pickFreeSlot, removeLocalSample,
    type Sample,
  } from '../lib/state/samples';
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
    commitSlices,
    clearSlicingForSlots,
    type SliceMode,
    type SliceLoopMode,
    type Division,
  } from '../lib/state/slicing';
  import { ensureSampleLoaded, refreshCatalog } from '../lib/state/catalog';
  import { scanMemory } from '../lib/state/memory';
  import * as preview from '../lib/preview';
  import { buildWaveformPaths, buildSyntheticPaths } from '../lib/waveform';
  import { get } from 'svelte/store';
  import { onMount, onDestroy } from 'svelte';
  // Wails bindings — regenerated on `wails dev` boot. The new
  // slicing methods land here after the Go side compiles.
  import * as App from '../../wailsjs/go/main/App';
  import { EventsOn, OnFileDrop, OnFileDropOff } from '../../wailsjs/runtime/runtime';

  onMount(() => setSync('synced', 'Synced'));

  // ---------- Modal flow ----------
  // Two parallel modals: the stub one (Get/Send/Replace/etc. — not
  // wired yet) and the real Apply-slicing flow (Inspect → Preflight
  // → Apply → Transfer with live progress events).
  type ModalKind = 'closed' | 'transfer';
  let modalKind: ModalKind = 'closed';
  function openTransfer() { modalKind = 'transfer'; }
  function cancel()       { modalKind = 'closed'; }

  // Import: decode + clamp + convert audio and stash on the selected
  // sample's slot. Bypasses livesync deliberately — the device doesn't
  // have this audio yet (only the slicing Apply pushes new samples
  // up), so a stray SPRM writeback would either NAK or describe
  // phantom audio. Host-side Web Audio preview means PCM never leaves
  // memory until Apply Slicing.
  //
  // path === '' opens the native file picker; a non-empty path
  // (drag-and-drop) imports that file directly. mode picks placement:
  //   • 'replace' — overwrite the currently selected slot in place.
  //     Falls back to 'add' if nothing is selected (empty list).
  //   • 'add'     — always allocate a new free slot, never touch the
  //     existing selection. Used by the sidebar drop target so the
  //     "Samples" list grows instead of mutating the row the user
  //     happens to have highlighted.
  async function importSample(path = '', mode: 'replace' | 'add' = 'replace') {
    setSync('sending', 'Importing...');
    try {
      const info = await (App as any).ImportSample(path);
      if (!info) {
        setSync('synced', 'Synced');
        return;
      }
      const list = get(samples);
      const curSlot = get(selectedSampleSlot);
      const existing = list.find((x) => x.slot === curSlot);
      // Replace path: only when the caller asked for it AND there IS
      // something to replace. Otherwise fall through to allocate a new
      // slot — drop-onto-empty-sidebar lands at the lowest free slot.
      if (mode === 'replace' && existing) {
        samples.update((xs) =>
          xs.map((x) =>
            x.slot === curSlot
              ? {
                  ...x,
                  name: info.name,
                  rate: info.rate,
                  length: info.length,
                  start: 0,
                  end: info.length,
                  loopStart: 0,
                  loopLength: 0,
                  pcm: info.pcm,
                  words12: info.words,
                  source: 'local',
                }
              : x,
          ),
        );
      } else {
        const slot = pickFreeSlot(list);
        if (slot < 0) {
          setSync('error', 'All 100 slots occupied — free one before importing');
          return;
        }
        const next: Sample = {
          ...newLocalSample(slot, info.name, info.rate, info.length),
          pcm: info.pcm,
          words12: info.words,
        };
        samples.update((xs) => [...xs, next].sort((a, b) => a.slot - b.slot));
        selectedSampleSlot.set(slot);
      }
      const secs = (info.length / info.rate).toFixed(2);
      setSync('synced', `Imported · ${secs}s · local only`);
    } catch (e: any) {
      setSync('error', String(e?.message ?? e));
    }
  }

  // Wails fires OnFileDrop when a file lands on an element with the
  // CSS `--wails-drop-target: drop` property. We take the first
  // audio-looking path and reuse importSample() — multi-file drops
  // are out of scope for now (would need to allocate consecutive
  // slots, similar to slicing). Non-audio extensions get a setSync
  // error so the user knows the drop was seen but ignored.
  //
  // Wails delivers a single OnFileDrop callback regardless of which
  // dropzone fired, so we track the most recently entered zone via
  // the per-target dragenter handlers and consult it here to choose
  // between "add new" (sidebar) and "replace selected" (waveform).
  const AUDIO_EXT = /\.(wav|wave|aif|aiff)$/i;
  function onFileDrop(paths: string[]) {
    if (!paths || paths.length === 0) return;
    const first = paths.find((p) => AUDIO_EXT.test(p));
    if (!first) {
      setSync('error', 'Drop a .wav or .aif file');
      lastDropTarget = null;
      return;
    }
    const mode = lastDropTarget === 'sidebar' ? 'add' : 'replace';
    lastDropTarget = null;
    void importSample(first, mode);
  }

  onMount(() => {
    // useDropTarget=true → only drops on opted-in elements (those
    // with style="--wails-drop-target: drop") fire the callback. The
    // rest of the window ignores drops, which matches the visual
    // affordance of the dropzone hints.
    OnFileDrop((_x, _y, paths) => {
      // Drop fired — clear the hover state in case dragleave didn't
      // fire (some platforms suppress it once the drop happens).
      dragDepth = 0;
      onFileDrop(paths);
    }, true);
  });
  onDestroy(() => {
    OnFileDropOff();
  });

  // ---------- Drag-over hover state ----------
  // Wails has no native "hovering valid target" event, so we ride the
  // standard HTML5 drag events. They fire in the WebView for OS-level
  // file drags even though the file payload isn't accessible there
  // (Wails delivers paths via OnFileDrop). dragDepth is a per-target
  // counter — enter increments, leave decrements — so traversing
  // nested children doesn't flicker the hover class on and off.
  //
  // lastDropTarget is set by the per-zone dragenter handlers and
  // read once by onFileDrop to pick add vs replace semantics. The
  // sidebar zone resolves to 'add' (grow the list), the waveform
  // and empty-state zones resolve to 'replace' (overwrite current
  // sample, or fall through to add when nothing is selected).
  type DropTarget = 'sidebar' | 'waveform' | 'empty';
  let dragDepth = 0;
  let lastDropTarget: DropTarget | null = null;
  function onDragEnter(e: DragEvent, target: DropTarget) {
    if (!e.dataTransfer?.types.includes('Files')) return;
    dragDepth++;
    lastDropTarget = target;
  }
  function onDragLeave(e: DragEvent) {
    if (!e.dataTransfer?.types.includes('Files')) return;
    if (dragDepth > 0) dragDepth--;
  }
  function onDragOver(e: DragEvent) {
    // preventDefault is required for `drop` to fire in the DOM; we
    // don't actually use the DOM drop event (Wails wins), but
    // suppressing the browser's default "show NOT-ALLOWED cursor on
    // file drag" makes the affordance correct.
    if (e.dataTransfer?.types.includes('Files')) e.preventDefault();
  }
  $: isDragging = dragDepth > 0;

  // Get SPRM from S950: re-pull just the SPRM block for the
  // currently selected sample. Fast (~50ms), no modal needed.
  async function getSPRMFromDevice() {
    const slot = get(selectedSampleSlot);
    setSync('sending', 'Fetching...');
    try {
      await ensureSampleLoaded(slot, true);
      setSync('synced', 'Synced');
    } catch (e: any) {
      setSync('error', 'Fetch failed');
    }
  }

  // ---------- Apply Slicing ----------
  type SlicePhase = 'idle' | 'inspecting' | 'preflight' | 'applying' | 'done' | 'error';
  let slicePhase: SlicePhase = 'idle';
  let slicePreflight: any | null = null;
  let sliceProgress: any | null = null;
  let sliceError = '';
  let sliceUnsub: (() => void) | null = null;

  // True when the user has Commit-Slices'd this source already; the
  // returned children are sitting in the sidebar awaiting upload.
  // Apply uses them as the payload (preserving any names/loop tweaks
  // the user made post-commit) instead of re-extracting from source.
  $: committedChildren = $samples
    .filter((s) => s.parentSlot === smp.slot && s.source === 'local')
    .sort((a, b) => a.slot - b.slot);
  $: hasCommitted = committedChildren.length > 0;

  function buildSlicingRequest() {
    // Two upload paths:
    //   • Committed path — children rows exist in the sidebar. Concat
    //     their words12 into one virtual source and generate slice
    //     specs that point at each child's range. Names + per-slice
    //     loop configs come from the children, so post-commit edits
    //     are honoured by the backend extractor.
    //   • Direct path — no commit. Send the source's words12 + current
    //     slice config (the legacy in-one-shot flow).
    if (hasCommitted) {
      let offset = 0;
      const slices: any[] = [];
      const buf: number[] = [];
      for (const c of committedChildren) {
        const w = c.words12 ?? [];
        slices.push({
          name: c.name,
          startWord: offset,
          lengthWords: w.length,
          loopMode: c.mode,
          loopStart: c.loopStart,
          loopLength: c.loopLength,
        });
        for (let i = 0; i < w.length; i++) buf.push(w[i]);
        offset += w.length;
      }
      return {
        sourceWords: buf,
        sourceRateHz: smp.rate,
        slices,
        baseName: smp.name,
        programName: smp.name.slice(0, 10),
        baseMidiKey: 36,
        firstSampleSlot: -1,
        programSlot: -1,
      };
    }
    // Direct path — words12 is populated by ImportSample; missing
    // only when the current sample is the in-memory stub. The
    // backend's Inspect catches the empty-buffer case and reports it
    // as an error.
    const sourceWords = smp.words12 ?? [];
    return {
      sourceWords,
      sourceRateHz: smp.rate,
      slices: $slicing.slices.map((sl) => ({
        name: '', // Apply auto-generates "{base}_NN" when name is empty
        startWord: sl.start,
        lengthWords: sl.length,
        loopMode: sl.loopMode,
        loopStart: sl.loopStart,
        loopLength: sl.loopLength,
      })),
      baseName: smp.name,
      programName: smp.name.slice(0, 10),
      baseMidiKey: 36, // C2 — first slice maps here, rest chromatic
      // -1 is the auto-pick sentinel: backend finds the first run of
      // N consecutive free sample slots + the first free program
      // slot. Manual override (future toggle in the slicing UI) will
      // pass non-negative values here.
      firstSampleSlot: -1,
      programSlot: -1,
    };
  }

  async function startApplySlicing() {
    sliceError = '';
    sliceProgress = null;
    slicePhase = 'inspecting';
    try {
      const req = buildSlicingRequest();
      slicePreflight = await (App as any).InspectSlicing(req);
      slicePhase = 'preflight';
    } catch (e: any) {
      sliceError = String(e?.message ?? e);
      slicePhase = 'error';
    }
  }

  async function continueApplySlicing() {
    if (!slicePreflight || !slicePreflight.ok) return;
    sliceProgress = null;
    slicePhase = 'applying';

    // Subscribe BEFORE invoking — the very first progress event fires
    // before the await resolves, so missing it means no UI updates.
    sliceUnsub = EventsOn('slicing:progress', (p: any) => {
      sliceProgress = p;
      if (p.phase === 'done')  slicePhase = 'done';
      if (p.phase === 'error') { slicePhase = 'error'; sliceError = p.message; }
    });

    try {
      await (App as any).ApplySlicing(buildSlicingRequest());
      // On success, any committed children that fed this upload are
      // now duplicates — the device has the real samples at new
      // slots, and the local children should be discarded so the
      // sidebar doesn't show both.
      if (hasCommitted) {
        const childSlots = new Set(committedChildren.map((c) => c.slot));
        samples.update((xs) => xs.filter((s) => !childSlots.has(s.slot)));
      }
      // Drop the per-slot slicing state for the source slot AND
      // every destination slot we just wrote to. After Apply the
      // source slot has been overwritten by the first slice child
      // (same slot number, totally different sample) — without this
      // the parent's slice marks would render on top of the new
      // child's waveform, complete with start/length values that
      // overflow the child's tiny length. The destination slots
      // come from preflight's projected allocation.
      const sourceSlot = get(selectedSampleSlot);
      const destSlots: number[] = Array.isArray(slicePreflight.sampleSlots)
        ? slicePreflight.sampleSlots
        : [];
      clearSlicingForSlots([sourceSlot, ...destSlots]);
      // Refresh the catalog so the newly-uploaded slice samples +
      // their auto-generated program appear in the sidebars/lists
      // immediately. Without this, the user has to disconnect or
      // hit "Get from S950" to see what they just sent — confusing
      // because the sample list looks stale right after a
      // successful upload. Memory scan runs after, against the
      // refreshed sample list, so the topbar chip reflects the new
      // device occupancy. Fire-and-forget: the success modal
      // doesn't block on either.
      void (async () => {
        try { await refreshCatalog(); } catch {}
        try { await scanMemory();    } catch {}
      })();
    } catch (e: any) {
      sliceError = String(e?.message ?? e);
      slicePhase = 'error';
    } finally {
      if (sliceUnsub) { sliceUnsub(); sliceUnsub = null; }
    }
  }

  function cancelApply() {
    slicePhase = 'idle';
    slicePreflight = null;
    sliceProgress = null;
    sliceError = '';
    if (sliceUnsub) { sliceUnsub(); sliceUnsub = null; }
  }

  onDestroy(() => {
    if (sliceUnsub) sliceUnsub();
    // Kill any in-flight Web Audio preview when navigating away —
    // otherwise the source keeps playing through the new tab.
    preview.stop();
  });

  // EMPTY_SAMPLE is the type-safe fallback used while the sidebar is
  // empty (fresh app, no Connect, no imports). The reactive
  // derivations below all need a defined Sample to avoid runtime
  // throws; the actual empty-state UI is gated separately via
  // `hasSample` so the user sees the call-to-action, not these
  // zeroed-out numbers.
  const EMPTY_SAMPLE: Sample = {
    slot: -1, name: '', rate: 26040, length: 1,
    start: 0, end: 1, loopStart: 0, loopLength: 0,
    mode: 'one-shot', reverse: false, velXfade: false,
    tune: 0, loudness: 0, source: 'local',
  };
  $: smp = $selectedSample ?? EMPTY_SAMPLE;
  $: hasSample = !!$selectedSample;

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
  // Switching a slice from one-shot → loop/ping-pong seeds reasonable
  // loop bounds so the band is immediately visible and grabbable. We
  // only auto-place when there's no loop yet (loopLength == 0); if
  // the user previously set a loop and switched to one-shot, their
  // values stick around for the next switch back.
  function setSliceLoopMode(idx: number, mode: SliceLoopMode) {
    const sl = $slicing.slices[idx];
    if (!sl) return;
    if (mode !== 'one-shot' && sl.loopLength === 0) {
      const start = Math.floor(sl.length / 2);
      updateSliceAt(idx, {
        loopMode: mode,
        loopStart: start,
        loopLength: sl.length - start,
      });
    } else {
      updateSliceAt(idx, { loopMode: mode });
    }
  }

  function previewSlice(idx: number) {
    const s = $slicing.slices[idx];
    if (!s) return;
    selectSlice(idx);
    preview.previewSlice(smp, s);
  }

  // Whole-sample preview from the identity strip's ▶ button. Plays
  // the [Start..End] window honouring the SPRM replay mode + loop.
  function previewWhole() {
    preview.previewSample(smp);
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

  // ---------- Per-slice loop edge drag ----------
  // Grab the left edge of a slice's loop band → moves loopStart while
  // holding the loop's right edge in place. Grab the right edge →
  // changes loopLength. Bounded inside [0..slice.length].
  type LoopDrag = {
    idx: number;
    edge: 'start' | 'end';
    startX: number;
    startLoopStart: number;
    startLoopLength: number;
    moved: boolean;
  };
  let loopDrag: LoopDrag | null = null;

  function onLoopHandleDown(e: MouseEvent, idx: number, edge: 'start' | 'end') {
    if (e.button !== 0) return;
    e.preventDefault();
    e.stopPropagation();
    selectSlice(idx);
    const sl = $slicing.slices[idx];
    loopDrag = {
      idx, edge,
      startX: e.clientX,
      startLoopStart: sl.loopStart,
      startLoopLength: sl.loopLength,
      moved: false,
    };
  }

  function onLoopDragMove(e: MouseEvent) {
    if (!loopDrag || !waveformEl) return;
    const r = waveformEl.getBoundingClientRect();
    const wordsPerPx = visibleN / r.width;
    const dx = e.clientX - loopDrag.startX;
    if (Math.abs(dx) > 1) loopDrag.moved = true;
    const delta = Math.round(dx * wordsPerPx);
    const slices = $slicing.slices;
    const sl = slices[loopDrag.idx];
    if (!sl) return;

    // The slice's *actual* hard right edge — in slice-local words —
    // is the gap to the next slice's start (or sample end for the
    // last slice). Using sl.length here drifts whenever a manual
    // Length edit pushes it past the neighbour boundary, and the
    // loop would happily extend into the next slice. Compute from
    // neighbours so the bound stays correct.
    const nextStart = loopDrag.idx < slices.length - 1
      ? slices[loopDrag.idx + 1].start
      : smp.length;
    const maxSliceLocal = Math.max(1, nextStart - sl.start);

    if (loopDrag.edge === 'start') {
      // Hold the loop's right edge fixed; start can move within
      // [0, oldEnd], where oldEnd never exceeds the slice's hard
      // right edge (the user can't drag loopEnd past maxSliceLocal,
      // so oldEnd ≤ maxSliceLocal is already enforced on creation).
      const oldEnd = Math.min(
        maxSliceLocal,
        loopDrag.startLoopStart + loopDrag.startLoopLength,
      );
      let newStart = loopDrag.startLoopStart + delta;
      newStart = Math.max(0, Math.min(oldEnd, newStart));
      const newLength = oldEnd - newStart;
      updateSliceAt(loopDrag.idx, { loopStart: newStart, loopLength: newLength });
    } else {
      // Right edge — grow/shrink loopLength. Clamp the loop's end
      // (loopStart + loopLength) at maxSliceLocal so it cannot
      // cross into the next slice.
      let newLength = loopDrag.startLoopLength + delta;
      newLength = Math.max(0, Math.min(maxSliceLocal - sl.loopStart, newLength));
      updateSliceAt(loopDrag.idx, { loopLength: newLength });
    }
  }

  function onLoopDragUp() {
    if (!loopDrag) return;
    const wasMoved = loopDrag.moved;
    loopDrag = null;
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
    // Zoom is useful in both modes: slicing needs it to place slices
    // precisely; the default Markers view benefits when nudging
    // Start/End/Loop on long samples. The slicing store still owns
    // the zoom state per-sample either way.
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

  // Spacebar previews the selected slice (slicing active) or the
  // whole sample. Delete/Backspace removes the selected slice. Both
  // ignore an input/textarea focus so typing values in NumFields
  // isn't intercepted.
  function onWindowKeyDown(e: KeyboardEvent) {
    const t = e.target as HTMLElement | null;
    const inField = !!t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable);
    if (e.key === ' ' || e.code === 'Space') {
      if (inField) return;
      e.preventDefault();
      // Toggle: a second tap on space while looping playback is in
      // flight should stop, not stack another source on top.
      if (preview.isPlaying()) {
        preview.stop();
        return;
      }
      if ($slicing.active && $slicing.selectedIndex >= 0) {
        previewSlice($slicing.selectedIndex);
      } else {
        previewWhole();
      }
      return;
    }
    if (!$slicing.active) return;
    if (e.key !== 'Delete' && e.key !== 'Backspace') return;
    if (inField) return;
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
  // When the sample has imported audio (`smp.pcm` populated by
  // ImportSample), draw a real min/max peak envelope across the
  // visible window. When PCM isn't loaded (a catalog entry from the
  // device that wasn't host-imported), fall back to a slot-seeded
  // synthetic shape so the canvas still has something to render.
  //
  // Re-runs whenever the slot, the audio, or the zoom window
  // changes — peak aggregation is re-computed against the visible
  // sample range each time, so zooming in actually shows finer
  // detail instead of stretching the same coarse path.
  $: ({ top: wfTop, bot: wfBot } = (smp.pcm && smp.pcm.length > 0)
    ? buildWaveformPaths(smp.pcm, viewStart, visibleN)
    : buildSyntheticPaths(smp.slot));

  // Ruler ticks every ~70ms across the waveform.
  // Ruler step picks the smallest "nice" interval that keeps the
  // total tick count near targetTicks (≈8). Without this, a fixed
  // 70ms step produces 80+ ticks for a 6-second sample and the
  // labels become an illegible smear. Adapts to the visible window
  // (visibleSec) so zoomed-in views show finer ticks.
  $: visibleSec = visibleN / smp.rate;
  function pickRulerStep(span: number, targetTicks = 8): number {
    if (span <= 0) return 1;
    const raw = span / targetTicks;
    const steps = [0.01, 0.02, 0.05, 0.1, 0.2, 0.25, 0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300];
    for (const s of steps) if (s >= raw) return s;
    return Math.ceil(raw / 60) * 60;
  }
  function fmtRulerLabel(t: number, step: number): string {
    if (step < 1)  return `${t.toFixed(2)}s`;
    if (step < 10) return `${t.toFixed(1)}s`;
    return `${Math.round(t)}s`;
  }
  $: ruler = (() => {
    const span = visibleSec > 0 ? visibleSec : durationSec;
    const step = pickRulerStep(span);
    const out: Array<{ left: number; label: string }> = [];
    // Start at the first tick ≥ viewStart's time (so zoomed views
    // still anchor to round numbers like 1.0s, not 1.07s).
    const startSec = viewStart / smp.rate;
    const firstTick = Math.ceil(startSec / step) * step;
    for (let t = firstTick; t < startSec + span; t += step) {
      const left = ((t * smp.rate - viewStart) / visibleN) * 100;
      if (left < 0 || left > 100) continue;
      out.push({ left, label: fmtRulerLabel(t, step) });
    }
    return out;
  })();
</script>

<svelte:window
  on:mousemove={(e) => { onMarkerMove(e); onSliceDragMove(e); onLoopDragMove(e); }}
  on:mouseup={() => { onMarkerUp(); onSliceDragUp(); onLoopDragUp(); }}
  on:keydown={onWindowKeyDown} />

<div class="app app--3row">
  <Topbar slotCount={`${$samples.length} / 100`} slotNoun="samples" />

  <aside class="sidebar">
    <!-- The whole sidebar content sits in a single dashed white card
         that mirrors the main pane's empty-state shape, so the two
         panels feel like one visual system. The card itself is the
         file-drop target — dropping anywhere on it imports. -->
    <div class="sidebar__panel"
      class:is-dragging={isDragging}
      style="--wails-drop-target: drop;"
      on:dragenter={(e) => onDragEnter(e, 'sidebar')}
      on:dragleave={onDragLeave}
      on:dragover={onDragOver}>
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
            <span class="sample__name">{s.name || '(unnamed)'}</span>
            {#if s.source === 'local'}
              <!-- LOCAL tag + close button for un-uploaded imports.
                   Sits in the same column as the rate so device
                   samples still show kHz — the two never apply at the
                   same time (locals always have a known rate but the
                   tag is more important info). Delete is local-only
                   since the S950 has no remote-delete SysEx opcode. -->
              <span class="sample__tag sample__tag--local" title="Imported but not yet on the S950">LOCAL</span>
              <button
                type="button"
                class="sample__del"
                title="Remove this local sample (does not touch the S950)"
                aria-label={`Remove local sample ${s.name || s.slot}`}
                on:click|stopPropagation={() => removeLocalSample(s.slot)}>×</button>
            {:else}
              <span class="sample__rate">{Math.round(s.rate / 1000)}k</span>
            {/if}
          </div>
        {/each}
        {#if $samples.length === 0}
          <div class="sample-list__empty">
            No samples.<br/>Drop a file or connect to S950.
          </div>
        {/if}
      </div>
      <div class="sidebar__hint">
        {#if $samples.length === 0}
          drag .wav / .aiff files here<br/>to import
        {:else}
          drag .wav / .aiff files here<br/>to add a new sample<br/>
          <span class="sidebar__hint__alt">(drop on the waveform to replace)</span>
        {/if}
      </div>
    </div>
  </aside>

  <main class="main">
    {#if !hasSample}
      <!-- Empty state: sidebar holds nothing yet (fresh app, not
           connected, no imports). The whole panel acts as a drop
           target, so a single drag-and-drop creates the first row
           without the user having to think about slot assignment. -->
      <div class="empty-state"
        class:is-dragging={isDragging}
        style="--wails-drop-target: drop;"
        on:dragenter={(e) => onDragEnter(e, 'empty')}
        on:dragleave={onDragLeave}
        on:dragover={onDragOver}>
        <div class="empty-state__title">No samples loaded</div>
        <p class="empty-state__hint">
          Drop a <strong>.wav</strong> or <strong>.aif</strong> file anywhere on this panel,
          or connect to an S950 to pull its sample catalog.
        </p>
        <button type="button" class="btn btn--primary" on:click={() => importSample()}>
          Import .wav / .aiff...
        </button>
      </div>
    {:else}
    <!-- Identity strip. Card chrome (title, subtitle, big padding)
         dropped to give the waveform + Slices card more vertical
         room — the metadata is repeated in the topbar slot chip
         anyway. Inline label/value rows in a single tight strip. -->
    <section class="card identity-card">
      <div class="identity">
        <div class="identity__cell">
          <span class="row__label">Name</span>
          <span class="field field--wide field--yellow">
            <input type="text" value={smp.name} on:input={onNameInput} maxlength="10" aria-label="Sample name" />
          </span>
        </div>
        <div class="identity__cell">
          <span class="row__label">Slot</span>
          <span class="field field--source-{smp.source}">
            {smp.slot.toString().padStart(2, '0')}
            <span class="source-dot" aria-hidden="true"></span>
            <span class="source-tag">{smp.source === 'local' ? 'local only' : 'on S950'}</span>
          </span>
        </div>
        <div class="identity__cell">
          <span class="row__label">Rate</span>
          <span class="field">{(smp.rate / 1000).toFixed(2)} kHz</span>
        </div>
        <div class="identity__cell">
          <span class="row__label">Length</span>
          <span class="field">{smp.length.toLocaleString()} words</span>
        </div>
        <div class="identity__cell">
          <span class="row__label">Pitch</span>
          <span class="field">C3 (960)</span>
        </div>
        <div class="identity__cell identity__cell--preview">
          <span class="row__label">Preview</span>
          <button
            type="button"
            class="identity__play"
            on:click={previewWhole}
            disabled={!preview.hasHostAudio(smp)}
            title={preview.hasHostAudio(smp)
              ? 'Play (Start → End) · spacebar'
              : 'Import .wav / .aiff to enable host-side preview'}
            aria-label="Preview sample">▶</button>
        </div>
      </div>
    </section>

    <!-- Waveform card. The Wails CSS drop-target property covers the
         whole card (waveform + legend) so the natural target — the
         visible audio area — accepts file drops in addition to the
         sidebar hint. -->
    <section class="card waveform-card"
      class:is-dragging={isDragging}
      style="--wails-drop-target: drop;"
      on:dragenter={(e) => onDragEnter(e, 'waveform')}
      on:dragleave={onDragLeave}
      on:dragover={onDragOver}>
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
          {/if}
          <!-- Zoom segment lives outside the slicing-only block so it
               also works in the default Markers view — useful when
               nudging Start/End/Loop on long samples. -->
          <div class="slice-tools__group">
            <span class="slice-tools__label">zoom</span>
            <span class="seg">
              <button type="button" class={$slicing.zoom === 1 ? 'on' : ''}  on:click={() => updateSlicing({ zoom: 1 })}>1×</button>
              <button type="button" class={$slicing.zoom === 2 ? 'on' : ''}  on:click={() => updateSlicing({ zoom: 2 })}>2×</button>
              <button type="button" class={$slicing.zoom === 4 ? 'on' : ''}  on:click={() => updateSlicing({ zoom: 4 })}>4×</button>
              <button type="button" class={$slicing.zoom === 8 ? 'on' : ''}  on:click={() => updateSlicing({ zoom: 8 })}>8×</button>
            </span>
          </div>
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
      <!-- The waveform's click affordances (drop slice in manual mode,
           drag markers) have no meaningful keyboard analogue — slice
           drop is anchored on the mouse cursor position. Numeric edits
           of Start/End/Loop via NumFields cover the keyboard path. -->
      <!-- svelte-ignore a11y-click-events-have-key-events -->
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
        <!-- Mirror horizontally when the sample's Reversed SPRM flag
             is on — the squiggle's shape is still synthetic, but the
             direction it plays back IS real. -->
        <svg
          class="waveform__svg"
          class:waveform__svg--reversed={smp.reverse}
          viewBox="{viewBoxX} 0 {viewBoxW} 200"
          preserveAspectRatio="none" aria-hidden="true">
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
            <!-- Per-slice loop regions. Only drawn for slices whose
                 loopMode is 'loop' or 'ping-pong'; one-shot slices get
                 no band. Position is the slice's loop window in
                 source-word space, projected through the visible view. -->
            {#each $slicing.slices as sl, i (i)}
              {#if sl.loopMode !== 'one-shot' && sl.loopLength > 0}
                <div
                  class="slice-loop slice-loop--{sl.loopMode === 'ping-pong' ? 'ping' : 'loop'}"
                  style="left: {pct(sl.start + sl.loopStart)}%; width: {(sl.loopLength / visibleN) * 100}%;">
                  <!-- Edge handles: drag the left edge to move loopStart
                       (keeping the right edge fixed), drag the right to
                       grow/shrink loopLength. The band itself stays
                       click-through so it doesn't intercept slice-marker
                       clicks behind it. -->
                  <span class="slice-loop__handle slice-loop__handle--left"
                    on:mousedown={(e) => onLoopHandleDown(e, i, 'start')}></span>
                  <span class="slice-loop__handle slice-loop__handle--right"
                    on:mousedown={(e) => onLoopHandleDown(e, i, 'end')}></span>
                </div>
              {/if}
            {/each}

            {#each $slicing.slices as sl, i (i)}
              <div
                class="slice-marker {i === $slicing.selectedIndex ? 'is-selected' : ''}"
                style="--x: {pct(sl.start)}%;"
                on:mousedown={(e) => onSliceMarkerDown(e, i)}
                on:keydown={(e) => e.key === 'Enter' && selectSlice(i)}
                role="button"
                tabindex="0">
                <!-- Double-click on the head deletes the slice (Ableton
                     parity). The drag handler on the parent runs on
                     mousedown but only commits if the pointer actually
                     moved >1px, so the dblclick's two mousedown/mouseup
                     pairs don't shift the slice. Disabled at 1 slice. -->
                <span
                  class="slice-marker__head"
                  title={$slicing.slices.length > 1
                    ? 'Drag to move · double-click to delete'
                    : 'Drag to move'}
                  on:dblclick|stopPropagation|preventDefault={() => {
                    if ($slicing.slices.length > 1) removeSliceAt(i);
                  }}>{String(i + 1).padStart(2, '0')}</span>
                <span class="slice-marker__line"></span>
              </div>
            {/each}
          {/if}

          <!-- Hover cursor. In slicing mode shows where the next
               click would drop a slice; in default mode it's the
               zoom anchor + word-position readout. Hidden when the
               mouse isn't over the waveform. -->
          {#if cursorRatio !== null}
            <div class="waveform__cursor" style="left: {cursorRatio * 100}%;">
              <span class="waveform__cursor__badge">
                {cursorWord?.toLocaleString()}{$slicing.zoom > 1 ? ` · ${Math.round($slicing.zoom * 100)}%` : ''}
              </span>
            </div>
          {/if}

          <div class="waveform__ruler">
            {#each ruler as r}
              <span style="left: {r.left}%">{r.label}</span>
            {/each}
          </div>
        </div>
      </div>
      <div class="waveform__legend">
        <!-- When PCM is loaded we render a real peak envelope; when
             only the SPRM metadata is known (catalog entry, not yet
             host-imported) we fall back to a synthetic squiggle so
             the canvas isn't empty. The tag tells the user which. -->
        <span class="waveform__preview-tag">
          {smp.pcm && smp.pcm.length > 0 ? 'waveform · imported audio' : 'preview · synthetic waveform'}
        </span>
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

    <!-- Three columns when editing a single sample: Markers / Playback /
         Device. When slicing is active, the first two collapse into one
         merged "Slicing" workspace card (list | divider | selected
         slice props) — the source's Playback flags are irrelevant once
         each slice becomes its own sample on the device. -->
    <section class="controls" class:controls--slicing={$slicing.active}>
      {#if !$slicing.active}
        <div class="card">
          <div class="card__head">
            <div class="card__title">Markers</div>
            <div class="card__subtitle">in sample words</div>
          </div>
          <div class="row">
            <span class="row__label">Start</span>
            <NumField
              value={smp.start}
              min={0} max={smp.end}
              format={(v) => v.toLocaleString()}
              on:change={(e) => selectedSample.update({ start: e.detail })} />
          </div>
          <div class="row">
            <span class="row__label">End</span>
            <NumField
              value={smp.end}
              min={smp.start} max={smp.length}
              format={(v) => v.toLocaleString()}
              on:change={(e) => selectedSample.update({ end: e.detail })} />
          </div>
          <div class="row">
            <span class="row__label">Loop start</span>
            <NumField
              value={smp.loopStart}
              min={0} max={smp.length}
              format={(v) => v.toLocaleString()}
              on:change={(e) => selectedSample.update({ loopStart: e.detail })} />
          </div>
          <div class="row">
            <span class="row__label">Loop length</span>
            <NumField
              value={smp.loopLength}
              min={0} max={smp.length}
              format={(v) => v.toLocaleString()}
              on:change={(e) => selectedSample.update({ loopLength: e.detail })} />
          </div>
        </div>

        <div class="card">
          <div class="card__head">
            <div class="card__title">Playback</div>
            <div class="card__subtitle">SPRM replay flags</div>
          </div>
          <!-- Mode lives in a flex row instead of the 120-px label grid
               so all three toggles fit at narrow column widths. -->
          <div class="row row--inline">
            <span class="row__label">Mode</span>
            <div class="mode-row">
              <button type="button" class="toggle {smp.mode === 'one-shot' ? 'on' : ''}" on:click={() => selectedSample.update({ mode: 'one-shot' })}>Once</button>
              <button type="button" class="toggle {smp.mode === 'loop' ? 'on' : ''}"     on:click={() => selectedSample.update({ mode: 'loop' })}>Loop</button>
              <button type="button" class="toggle {smp.mode === 'ping-pong' ? 'on' : ''}" on:click={() => selectedSample.update({ mode: 'ping-pong' })}>Ping-pong</button>
            </div>
          </div>
          <div class="row">
            <span class="row__label">Reverse</span>
            <button type="button" class="toggle {smp.reverse ? 'on' : ''}" on:click={() => selectedSample.update({ reverse: !smp.reverse })}>
              {smp.reverse ? 'On' : 'Off'}
            </button>
          </div>
          <div class="row">
            <span class="row__label">Vel x-fade</span>
            <button type="button" class="toggle {smp.velXfade ? 'on' : ''}" on:click={() => selectedSample.update({ velXfade: !smp.velXfade })}>
              {smp.velXfade ? 'On' : 'Off'}
            </button>
          </div>

          <hr class="panel__divider" />

          <div class="row">
            <span class="row__label">Tune</span>
            <NumField
              value={smp.tune}
              min={-50} max={50} step={0.01}
              format={(v) => `${v >= 0 ? '+' : ''}${v.toFixed(2)}`}
              on:change={(e) => selectedSample.update({ tune: e.detail })} />
          </div>
          <div class="row">
            <span class="row__label">Loudness</span>
            <NumField
              value={smp.loudness}
              on:change={(e) => selectedSample.update({ loudness: e.detail })} />
          </div>
        </div>
      {:else}
        {@const sel = $slicing.slices[$slicing.selectedIndex]}
        <!-- Merged slicing workspace: one outer card chrome, two inner
             columns separated by a hairline divider. The left column is
             list-only (stable height, no layout shift when loop-mode
             rows appear); the right shows the selected slice's props. -->
        <div class="card slice-pair">
          <div class="slice-pair__col">
            <div class="card__head">
              <div class="card__title">Slices</div>
              <div class="card__subtitle">{$slicing.slices.length} / {MAX_SLICES}</div>
            </div>
            <div class="slice-list slice-list--tall">
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
                    disabled={!preview.hasHostAudio(smp)}
                    title={preview.hasHostAudio(smp)
                      ? 'Preview slice'
                      : 'Import audio to enable preview'}
                    on:click|stopPropagation={() => previewSlice(i)}>▶</button>
                  <!-- Per-row delete. Disabled at 1 slice because
                       removeSliceAt refuses to drop below one (the
                       waveform always needs a covering span). The
                       neighbouring slice expands into the gap so the
                       sample's full range stays covered. -->
                  <button
                    type="button"
                    class="slice-list__del"
                    aria-label={`Delete slice ${i + 1}`}
                    disabled={$slicing.slices.length <= 1}
                    title={$slicing.slices.length <= 1
                      ? 'Need at least one slice'
                      : 'Delete slice (neighbour expands to fill)'}
                    on:click|stopPropagation={() => removeSliceAt(i)}>×</button>
                </div>
              {/each}
            </div>
          </div>
          <div class="slice-pair__col">
            <div class="card__head">
              <div class="card__title">Selected slice</div>
              <div class="card__subtitle">
                {#if sel}slot allocated on Apply{:else}none selected{/if}
              </div>
            </div>
            {#if sel}
              <div class="row">
                <span class="row__label">Slice</span>
                <NumField
                  value={$slicing.selectedIndex + 1}
                  min={1} max={$slicing.slices.length}
                  format={(v) => `${String(v).padStart(2, '0')} of ${$slicing.slices.length}`}
                  on:change={(e) => selectSlice(e.detail - 1)} />
              </div>
              <div class="row">
                <span class="row__label">Start</span>
                <NumField
                  value={sel.start}
                  min={0} max={smp.length}
                  format={(v) => v.toLocaleString()}
                  on:change={(e) => updateSliceAt($slicing.selectedIndex, { start: e.detail })} />
              </div>
              <div class="row">
                <span class="row__label">Length</span>
                <NumField
                  value={sel.length}
                  min={1} max={smp.length}
                  format={(v) => v.toLocaleString()}
                  on:change={(e) => updateSliceAt($slicing.selectedIndex, { length: e.detail })} />
              </div>
              <div class="row row--inline">
                <span class="row__label">Loop</span>
                <div class="mode-row">
                  {#each LOOP_MODES as m}
                    <button type="button" class="toggle {sel.loopMode === m ? 'on' : ''}"
                      on:click={() => setSliceLoopMode($slicing.selectedIndex, m)}>
                      {loopLabel(m)}
                    </button>
                  {/each}
                </div>
              </div>
              <!-- Loop start / length only matter for loop and ping-pong
                   modes — hide in one-shot to reduce clutter. Living in
                   the props column means the shift no longer pushes the
                   slice list around. -->
              {#if sel.loopMode !== 'one-shot'}
                <div class="row">
                  <span class="row__label">Loop start</span>
                  <NumField
                    value={sel.loopStart}
                    min={0} max={sel.length}
                    format={(v) => v.toLocaleString()}
                    on:change={(e) => updateSliceAt($slicing.selectedIndex, { loopStart: e.detail })} />
                </div>
                <div class="row">
                  <span class="row__label">Loop length</span>
                  <NumField
                    value={sel.loopLength}
                    min={0} max={sel.length}
                    format={(v) => v.toLocaleString()}
                    on:change={(e) => updateSliceAt($slicing.selectedIndex, { loopLength: e.detail })} />
                </div>
              {/if}
            {:else}
              <div class="slice-pair__empty">Select a slice to edit its boundaries + loop.</div>
            {/if}
          </div>
        </div>
      {/if}

      <div class="card">
        <div class="card__head">
          <div class="card__title">Device &amp; file</div>
          <div class="card__subtitle">MRCC Port 03 · slot {smp.slot.toString().padStart(2, '0')}</div>
        </div>
        <div class="actions actions--stack">
          {#if $slicing.active || hasCommitted}
            <!-- Headline action when slicing or already committed.
                 Commit materialises the slices as N local sidebar rows
                 (optional preview step). Apply uploads — either from
                 source words (no commit) or from the committed child
                 rows (post-commit). Children disappear on Apply
                 success, unlocking Commit for the next run. -->
            {#if $slicing.active && !hasCommitted}
              <button type="button" class="btn"
                disabled={!preview.hasHostAudio(smp) || $slicing.slices.length === 0}
                title={preview.hasHostAudio(smp)
                  ? 'Create N local sample rows from the current slices (no upload yet)'
                  : 'Import audio first'}
                on:click={() => commitSlices()}>
                Commit {$slicing.slices.length} slices to sidebar
              </button>
            {/if}
            <button type="button" class="btn btn--primary" on:click={startApplySlicing}>
              {#if hasCommitted}
                Apply → upload {committedChildren.length} samples + program
              {:else}
                Apply slicing → {$slicing.slices.length} samples + program
              {/if}
            </button>
            <hr class="panel__divider" />
          {/if}
          <button type="button" class="btn btn--primary" on:click={() => importSample()}>Import .wav / .aiff...</button>
          <button type="button" class="btn" on:click={openTransfer}>Replace from S950</button>
          <button type="button" class="btn" on:click={openTransfer}>Download .wav...</button>
          <hr class="panel__divider" />
          <button type="button" class="btn">Send SPRM to S950</button>
          <button type="button" class="btn" on:click={getSPRMFromDevice}>Get SPRM from S950</button>
        </div>
      </div>
    </section>
    {/if}
  </main>

  <Statusbar
    hints={[
      { key: 'drag', label: 'marker to nudge' },
      { key: 'scroll', label: 'zoom waveform' },
      { key: 'space', label: 'preview' },
      { key: '⌘↵', label: 'send to s950' },
    ]}
    status={hasSample
      ? `${smp.name} · slot ${smp.slot.toString().padStart(2, '0')} · ${smp.mode} · ${smp.length.toLocaleString()} words · ${smp.source === 'local' ? 'local only' : 'on S950'}`
      : 'no sample selected'}
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

<!-- ===== Apply slicing modals =====
     Three states share the same backdrop:
       inspecting → spinner-ish "checking device"
       preflight  → checklist with errors/warnings, Continue if OK
       applying   → live progress driven by slicing:progress events
       done/error → terminal state with Close
-->
{#if slicePhase !== 'idle'}
  <div class="modal-backdrop is-open" role="dialog" aria-modal="true">
    <div class="modal">
      <header class="modal__head">
        <h2 class="modal__title">
          {#if slicePhase === 'inspecting'}Inspecting <strong>{smp.name}</strong>
          {:else if slicePhase === 'preflight'}Ready to apply slicing — <strong>{smp.name}</strong>
          {:else if slicePhase === 'applying'}Applying slicing — <strong>{smp.name}</strong>
          {:else if slicePhase === 'done'}Done — <strong>{smp.name}</strong>
          {:else}Slicing error
          {/if}
        </h2>
        <div class="modal__route">{$slicing.slices.length} slices · MRCC Port 03 · Ch 0</div>
      </header>

      <div class="modal__body">
        {#if slicePhase === 'inspecting'}
          <div class="modal__step">Checking device catalog...</div>
        {:else if slicePhase === 'preflight' && slicePreflight}
          <ul class="preflight">
            <li class="preflight__item preflight__item--ok">
              <span class="preflight__icon">✓</span>
              <div class="preflight__body">
                <span class="preflight__title">Slice math</span>
                <span class="preflight__detail">{$slicing.slices.length} slices · all lengths ≥ 200 words</span>
              </div>
            </li>
            <li class="preflight__item preflight__item--ok">
              <span class="preflight__icon">✓</span>
              <div class="preflight__body">
                <span class="preflight__title">Slot allocation</span>
                <span class="preflight__detail">samples → slots {slicePreflight.sampleSlots?.[0]}...{slicePreflight.sampleSlots?.[slicePreflight.sampleSlots.length - 1]} · program → slot {String(slicePreflight.programSlot).padStart(2, '0')}</span>
              </div>
            </li>
            {#each (slicePreflight.errors ?? []) as err}
              <li class="preflight__item preflight__item--error">
                <span class="preflight__icon">✗</span>
                <div class="preflight__body">
                  <span class="preflight__title">Error</span>
                  <span class="preflight__detail">{err}</span>
                </div>
              </li>
            {/each}
            {#each (slicePreflight.warnings ?? []) as warn}
              <li class="preflight__item preflight__item--warn">
                <span class="preflight__icon">⚠</span>
                <div class="preflight__body">
                  <span class="preflight__title">Warning</span>
                  <span class="preflight__detail">{warn}</span>
                </div>
              </li>
            {/each}
          </ul>
          <div class="preflight__estimate">
            Estimated transfer: ~{Math.floor(slicePreflight.estimatedSeconds / 60)}m {slicePreflight.estimatedSeconds % 60}s
          </div>
        {:else if slicePhase === 'applying' || slicePhase === 'done'}
          <div class="modal__step">
            {sliceProgress?.phase === 'uploading_program' ? 'Building program' : sliceProgress?.phase === 'done' ? 'Complete' : 'Uploading slices'}
            <strong>{sliceProgress?.message ?? 'Starting...'}</strong>
          </div>
          <div class="progress">
            <div class="progress__bar" style="width: {sliceProgress?.percent ?? 0}%;"></div>
            <div class="progress__label">{sliceProgress?.percent ?? 0}%</div>
          </div>
          <div class="log">
            {#if sliceProgress}
              <div class="log__row {slicePhase === 'done' ? 'ok' : 'run'}">
                {slicePhase === 'done' ? '✓' : '▶'} {sliceProgress.message}
              </div>
              {#if sliceProgress.sliceIndex >= 0}
                <div class="log__row pending">Slice {sliceProgress.sliceIndex + 1} of {sliceProgress.totalSlices}</div>
              {/if}
            {:else}
              <div class="log__row pending">· Connecting...</div>
            {/if}
          </div>
        {:else if slicePhase === 'error'}
          <ul class="preflight">
            <li class="preflight__item preflight__item--error">
              <span class="preflight__icon">✗</span>
              <div class="preflight__body">
                <span class="preflight__title">Slicing failed</span>
                <span class="preflight__detail">{sliceError || 'Unknown error'}</span>
              </div>
            </li>
          </ul>
        {/if}
      </div>

      <footer class="modal__foot">
        {#if slicePhase === 'preflight'}
          <button type="button" class="btn" on:click={cancelApply}>Cancel</button>
          <button
            type="button"
            class="btn btn--primary"
            disabled={!slicePreflight?.ok}
            on:click={continueApplySlicing}>Continue ▶</button>
        {:else if slicePhase === 'applying'}
          <!-- No Cancel during applying. The S950 has no remote
               "delete sample" path, so an aborted run would leave
               orphan slices on the device with no clean rollback.
               Once Continue is clicked, the user waits. -->
          <span class="modal__waitnote">do not close · upload in progress</span>
        {:else}
          <button type="button" class="btn btn--primary" on:click={cancelApply}>Close</button>
        {/if}
      </footer>
    </div>
  </div>
{/if}

<style>
  /* Flex column. Identity is fixed at the top, controls fixed at the
     bottom, the waveform card flexes to whatever space is left.
     overflow: auto on main is the last-resort fallback for very
     short viewports — keeps the bottom cards from being clipped
     silently when the layout exceeds the available height. */
  .main {
    grid-area: main;
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 16px;
    background: var(--main-bg);
    height: 100%;
    min-height: 0;
    overflow: auto;
  }
  .identity-card {
    flex: 0 0 auto;
    /* Override the .card base padding — identity is a tight metadata
       strip, not a full card with title + body. */
    padding: 10px 14px;
  }
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
    /* Slot column gets minmax(140px, 1.4fr) so the slot number +
       sync dot + "local only" tag fit on one line. */
    grid-template-columns: 2fr minmax(140px, 1.4fr) 1fr 1fr 1fr auto;
    gap: 14px;
    align-items: center;
  }
  .identity__cell--preview { align-items: stretch; }
  .identity__play {
    width: 30px;
    height: 26px;
    border: 1px solid var(--black);
    border-radius: var(--r);
    background: var(--rb-yellow);
    color: var(--ink);
    font-size: 12px;
    cursor: pointer;
    padding: 0;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .identity__play:hover:not([disabled]) { background: var(--rb-magenta); color: var(--paper); }
  .identity__play[disabled] {
    background: var(--grey-light);
    color: var(--grey-medium);
    cursor: not-allowed;
  }
  /* Sample-tab specific empty-state styling is now in shared.css as
     .empty-state so Program + Keygroup tabs match. The drag-over
     `.is-dragging` class still drives the yellow-tinted hover
     feedback used by the file-drop affordance. */

  /* Source dot + tag inside the identity Slot field. The dot tracks
     the sync state at a glance; the tag spells it out for users who
     don't yet have the dot's meaning memorised. Magenta = local-only,
     green = device-committed. */
  .field--source-local .source-dot { background: var(--rb-orange); }
  .field--source-device .source-dot { background: var(--rb-green); }
  .source-dot {
    display: inline-block;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    border: 1px solid var(--black);
    margin-left: 4px;
    flex-shrink: 0;
  }
  .source-tag {
    font-family: var(--font-mono);
    font-size: 9px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--grey-dark);
    margin-left: 4px;
  }
  .field--source-local .source-tag { color: var(--black); font-weight: 700; }

  .identity__cell { display: flex; flex-direction: column; gap: 3px; }
  /* Identity strip's row__label is smaller + tighter than the standard
     .row__label so the metadata strip stays visually distinct. */
  .identity__cell > :global(.row__label) {
    font-size: 9px;
    letter-spacing: 0.08em;
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
  /* Reverse-sample mirror — synthetic shape flips so the visual
     read of playback direction matches the SPRM Reversed flag. */
  .waveform__svg--reversed { transform: scaleX(-1); }

  /* Subtle "this isn't real audio" tag, lives in the legend so the
     user knows the squiggle is synthetic while markers are real. */
  .waveform__preview-tag {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 1px 6px;
    border: 1px solid var(--grey-medium);
    border-radius: 999px;
    color: var(--grey-dark);
    font-style: italic;
    background: var(--white);
  }
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

  /* Three-column grid for the cards below the waveform. Sized so
     the Slices card fits its typical one-shot content without
     scrolling, while leaving the waveform comfortable room above.
     Loop / ping-pong slices show two extra rows that scroll within. */
  .controls {
    display: grid;
    grid-template-columns: 1fr 1fr 1fr;
    gap: 16px;
    flex: 0 0 auto;
    min-height: 0;
  }
  .controls :global(.card) {
    /* Bumped a touch now that the identity card no longer eats
       ~60px of vertical room — gives the Slices card breathing
       space for loop-mode rows without immediately scrolling. */
    max-height: 360px;
    overflow-y: auto;
  }

  /* Merged slicing workspace. One outer card spanning two grid columns;
     two inner halves divided by a hairline. padding: 0 lets the inner
     columns own their own padding so the divider runs full-height. */
  .slice-pair {
    grid-column: span 2;
    padding: 0;
    display: grid;
    grid-template-columns: 1fr 1fr;
    /* Override the .card overflow:auto + max-height inheritance: the
       outer card no longer scrolls; each inner column scrolls if it
       needs to (the slice list, mostly). */
    overflow: hidden;
  }
  .slice-pair__col {
    padding: 16px 18px;
    min-width: 0;
    display: flex;
    flex-direction: column;
    max-height: 360px;
    overflow-y: auto;
  }
  .slice-pair__col + .slice-pair__col {
    border-left: 1px solid var(--grey-light);
  }
  .slice-pair__empty {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--grey-dark);
    padding: 6px 0;
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
    color: var(--ink);
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
    color: var(--paper);
    border-color: var(--paper);
  }
  .slice-marker.is-selected .slice-marker__line {
    background: var(--rb-magenta);
    width: 2px;
    margin-left: -0.5px;
  }

  /* When slice mode is active:
       - the sample's Start/End markers are dimmed as context but
         stay visible (they bound the source the slices live in);
       - the sample's global Loop region + Loop-start/end markers
         disappear entirely — irrelevant since each slice now owns
         its own loop config rendered separately below.
       - the pre/post inactive dim also fades back, otherwise it
         hides the front of the audio behind a curtain.   */
  .waveform--slicing :global(.marker--start),
  .waveform--slicing :global(.marker--end),
  .waveform--slicing :global(.waveform__inactive) {
    opacity: 0.25;
    pointer-events: none;
  }
  .waveform--slicing :global(.waveform__loop),
  .waveform--slicing :global(.marker--loop-start),
  .waveform--slicing :global(.marker--loop-end) {
    display: none;
  }

  /* Per-slice loop region. Plain cyan band for forward loops; the
     ping-pong variant overlays diagonal hatching so you can tell at
     a glance which slices alternate without selecting them. */
  .slice-loop {
    position: absolute;
    top: 18px; bottom: 18px;
    background: color-mix(in srgb, var(--rb-cyan) 14%, transparent);
    border-left: 1px solid var(--rb-cyan);
    border-right: 1px solid var(--rb-cyan);
    pointer-events: none;
    z-index: 1;
  }
  .slice-loop--ping {
    background-image:
      linear-gradient(color-mix(in srgb, var(--rb-cyan) 14%, transparent),
                      color-mix(in srgb, var(--rb-cyan) 14%, transparent)),
      repeating-linear-gradient(
        45deg,
        transparent 0 5px,
        color-mix(in srgb, var(--rb-cyan) 22%, transparent) 5px 6px
      );
  }
  /* Edge handles for the loop band. 8px wide, overlapping the cyan
     border so they're easy to grab even when the band is narrow.
     pointer-events: auto re-enables hits inside the band (the band
     itself is click-through). */
  .slice-loop__handle {
    position: absolute;
    top: 0; bottom: 0;
    width: 8px;
    cursor: ew-resize;
    pointer-events: auto;
    background: transparent;
    z-index: 3;
  }
  .slice-loop__handle--left  { left: -4px; }
  .slice-loop__handle--right { right: -4px; }
  .slice-loop__handle:hover {
    background: var(--rb-cyan);
    opacity: 0.5;
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
    color: var(--ink);
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
  /* Taller variant used inside the merged .slice-pair workspace where
     the list owns the entire left column — gives room for ~15 slices
     before scrolling. */
  .slice-list--tall {
    max-height: 280px;
    flex: 1 1 auto;
  }
  .slice-list__row {
    display: grid;
    grid-template-columns: 28px 1fr auto 22px 22px;
    align-items: center;
    gap: 6px;
    padding: 3px 8px;
    cursor: pointer;
  }
  .slice-list__row + .slice-list__row { border-top: 1px solid var(--grey-light); }
  .slice-list__row:hover { background: var(--grey-light); }
  .slice-list__row.is-selected { background: var(--rb-yellow); color: var(--ink); }
  .slice-list__row.is-selected .slice-list__loop { color: var(--ink); }
  .slice-list__num { font-weight: 700; color: var(--grey-dark); }
  .slice-list__loop { color: var(--grey-dark); font-size: 10px; }
  .slice-list__play,
  .slice-list__del {
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
  .slice-list__play:hover:not([disabled]) { background: var(--rb-yellow); color: var(--ink); }
  /* Red wash on hover so the destructive action reads differently
     from the yellow preview button next to it. */
  .slice-list__del { font-size: 14px; line-height: 1; }
  .slice-list__del:hover:not([disabled]) { background: var(--rb-red); color: var(--paper); }
  .slice-list__play[disabled],
  .slice-list__del[disabled] {
    background: var(--grey-light);
    color: var(--grey-medium);
    cursor: not-allowed;
  }

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
