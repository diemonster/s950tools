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
    expectedSeconds,
    findZeroCrossing,
    MAX_SLICES,
    commitSlices,
    clearSlicingForSlots,
    captureSliceAudio,
    attachAudioToSamples,
    type SliceMode,
    type SliceLoopMode,
    type Division,
    type CapturedAudio,
  } from '../lib/state/slicing';
  import { ensureSampleLoaded, refreshCatalog, revertSampleToDevice } from '../lib/state/catalog';
  import { scanMemory } from '../lib/state/memory';
  import { transportKind, status as connectionStatus } from '../lib/state/connection';
  import { imgMode } from '../lib/state/imgmode';
  import { sampleToSampleParams } from '../lib/state/converters';
  import { programs } from '../lib/state/programs';

  // S950-friendly resample targets. The Lo-Fi group mirrors the
  // CLI's `--rate` aliases — these are the documented vibe presets
  // (SP1200, MPC60) plus telephone/lofi as anchors for extra-grainy
  // character. Standard group is the everyday rates a user might
  // want without crossing into bit-crush territory. All values
  // sit inside the S950's ~2k..65k Hz range; backend clamps if
  // anyone hand-feeds something outside.
  const RATE_LOFI_PRESETS: { label: string; rate: number }[] = [
    { label: 'MPC60 (40000)',    rate: 40000 },
    { label: 'SP1200 (26040)',   rate: 26040 },
    { label: 'Lofi (10000)',     rate: 10000 },
    { label: 'Telephone (8000)', rate: 8000  },
  ];
  const RATE_STANDARD_PRESETS: number[] = [48000, 44100, 32000, 22050, 16000];

  // resampleInFlight gates UI while the backend re-runs the
  // resampler. Pure host-side op (no wire traffic) so usually a
  // few hundred ms for small samples, longer for multi-MB ones.
  let resampleInFlight = false;
  async function onRateChange(e: Event) {
    if (!smp) return;
    const sel = e.currentTarget as HTMLSelectElement;
    const target = parseInt(sel.value, 10);
    sel.value = String(smp.rate); // snap-back so an error leaves it sane
    if (!Number.isFinite(target) || target === smp.rate) return;
    if (!smp.pcm || smp.pcm.length === 0) {
      // Device-only samples (no host PCM) can't be resampled
      // without first running Copy from S950. UI should already
      // hide the dropdown for those, but guard defensively.
      return;
    }
    resampleInFlight = true;
    try {
      const result = await (App as any).ResampleSample(smp.pcm, smp.rate, target);
      const oldLen = smp.length;
      const newLen = result.length;
      // Scale start/end/loop markers so the user's edit doesn't
      // get clobbered by the resample. ratio = newLen/oldLen;
      // floor to keep markers inside the new range. Loop length
      // can collapse to 0 (one-shot) at extreme downsampling —
      // that's fine, the device treats <5 as one-shot anyway.
      const ratio = newLen / Math.max(1, oldLen);
      const scaledStart      = Math.min(newLen, Math.floor(smp.start * ratio));
      const scaledEnd        = Math.min(newLen, Math.floor(smp.end   * ratio));
      // SPRM anchors the loop at Start; mirror that here so the
      // sample-type invariant (loopStart === start) holds after a
      // resample. Length scales independently.
      const scaledLoopStart  = scaledStart;
      const scaledLoopLength = Math.max(0, Math.floor(smp.loopLength * ratio));
      // Resampling diverges host audio from the device's stored
      // copy. Flip source to 'local' so the Sample-tab actions
      // switch from "Send SPRM" (params-only) to "Send to S950"
      // (full re-upload). Device-side audio in the same slot is
      // untouched until the user explicitly Sends.
      // parentSlot is intentionally NOT in the patch — the
      // selectedSample.update merge preserves it from the existing
      // sample so slice-children stay linked to their parent
      // through a resample.
      selectedSample.update({
        rate:       result.rate,
        length:     newLen,
        pcm:        result.pcm,
        words12:    result.words,
        start:      scaledStart,
        end:        scaledEnd,
        loopStart:  scaledLoopStart,
        loopLength: scaledLoopLength,
        source:     'local',
      });
    } catch (e) {
      console.error('ResampleSample failed:', e);
    } finally {
      resampleInFlight = false;
    }
  }

  // Short connection label for modal subtitles + identity strips.
  // Lifts the device-side identifier out of the connection store
  // so we don't have to format it inline at every call site. On
  // MIDI shows "<port> · Ch <n>"; on serial just the port path
  // (channel is moot on a point-to-point cable).
  $: connectionLabel = (() => {
    const s = $connectionStatus;
    if (!s.connected) return 'Not connected';
    if (s.kind === 'serial') return s.in ?? 'serial';
    return `${s.in ?? '?'} · Ch ${s.channel}`;
  })();
  import { persistSamplesToCache, wordsToPcm } from '../lib/state/waveformcache';
  import * as preview from '../lib/preview';
  import { playhead } from '../lib/preview';
  import { previewMode, PREVIEW_NOTE, PREVIEW_VELOCITY } from '../lib/state/preview-mode';
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
  // Revert wrapper. The sidebar's EDITED tag fires this; we just
  // delegate to the catalog action and shove errors at the console.
  // Failure here means the device link blipped — the user can retry.
  async function revertSample(slot: number) {
    try {
      await revertSampleToDevice(slot);
    } catch (e) {
      console.error(`Revert sample ${slot} failed:`, e);
    }
  }

  // Save .wav — writes the host PCM to disk via the OS file dialog.
  // Pairs with Import on the opposite direction. Disabled when the
  // sample has no host audio (device-only sample that hasn't been
  // Copy-from-S950'd yet, or pristine empty row). Async-fire-and-
  // forget; errors land in the console + a setSync chip flip.
  let saveWavBusy = false;
  async function saveWav() {
    if (!smp || !smp.pcm || smp.pcm.length === 0 || saveWavBusy) return;
    saveWavBusy = true;
    try {
      const written = await (App as any).SaveSampleWav(smp.name || `sample-${smp.slot}`, smp.rate, smp.pcm);
      if (written) setSync('synced', 'Saved .wav');
    } catch (e: any) {
      console.error('SaveSampleWav failed:', e);
      setSync('error', 'Save failed');
    } finally {
      saveWavBusy = false;
    }
  }

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

  // Copy from S950 (Phase 1C): pull SDATA for the currently selected
  // device sample, attach to the live store, persist to the
  // waveform cache. User-initiated, never automatic — SDATA dumps
  // are slow (seconds-to-minutes depending on sample size).
  //
  // **Progress UX**: rtmidi only surfaces complete SysEx envelopes,
  // so we cannot drive a "real" progress bar from received bytes.
  // Instead we estimate transfer time from sample size + MIDI baud
  // rate and tick a synthetic progress bar at 100ms. The estimate
  // is intentionally conservative — accounts for the per-block ACK
  // pump and modest driver buffering. Caps at 99% until the wire
  // call returns so the bar doesn't pretend to finish before the
  // last bytes land; on completion it snaps to 100%.
  // Disabled on LOCAL ONLY samples — there's nothing to copy from
  // the device side.
  type CopyPhase = 'idle' | 'copying' | 'done' | 'error';
  let copyPhase: CopyPhase = 'idle';
  let copyError = '';
  let copyStartMs = 0;
  let copyElapsedMs = 0; // ticked by setInterval while copying
  let copyTickHandle: ReturnType<typeof setInterval> | null = null;

  // Reactive % done — driven by elapsed-vs-estimated rather than
  // wire bytes (which we don't have until the F7 lands). Clamped
  // to 0..99 while in flight so the bar can't claim to be done
  // before the call returns; flips to 100 on success.
  $: copyPercent = (() => {
    if (copyPhase === 'done')  return 100;
    if (copyPhase === 'error') return 0;
    if (copyPhase !== 'copying' || !smp) return 0;
    const estMs = estimatedCopySeconds(smp.length) * 1000;
    if (estMs <= 0) return 0;
    const pct = (copyElapsedMs / estMs) * 100;
    // Slow asymptote past 95% so it doesn't look stalled if we
    // overshot the estimate — the device's per-block ACK loop adds
    // ~10-20ms beyond pure wire time.
    if (pct >= 95) return Math.min(99, 95 + (pct - 95) / 5);
    return Math.min(99, Math.max(0, pct));
  })();

  // Wire byte rate (bytes/sec) of the currently active transport.
  // MIDI is fixed at 31250 baud with 10-bit framing → 3125 B/s.
  // RS-232 uses whatever baud the status reports (typically 38400
  // or 50000 — see `set-baud` CLI + project memory). Falls back to
  // MIDI's rate when disconnected, so the modal still shows a
  // sensible figure before the user clicks Connect.
  $: wireBytesPerSecond = (() => {
    const s = $connectionStatus;
    if (s.connected && s.kind === 'serial' && s.baud) return s.baud / 10;
    return 3125; // MIDI default
  })();

  // estimatedCopySeconds projects a wall-clock duration for the SDS
  // dump receive. The number is the modal's progress-bar timeline,
  // so being accurate matters — the bar otherwise either slams to
  // 100% early (MIDI underestimate) or crawls past the actual
  // finish (RS-232 overestimate).
  //
  // Empirical overheads above pure wire time:
  //   • MIDI pump path: ~28% (40601-word sample = 33.8s vs 26.4s
  //     raw). Per-block ACK-pump latency dominates.
  //   • RS-232 streaming path: ~18% (164060-word sample = 79s vs
  //     67s raw at 50000 baud).
  // 1.2× is a single coefficient that's honest for both within a
  // few seconds — the per-block sync path on RS-232 wastes less
  // time but the device's internal processing per ACK is similar.
  function estimatedCopySeconds(words: number): number {
    // S950 wire envelope: 19-byte header + ceil(words/60) * 122-byte
    // blocks + closing F7.
    const blocks = Math.ceil(words / 60);
    const bytes = 20 + blocks * 122;
    const rawSec = bytes / wireBytesPerSecond;
    return Math.ceil(rawSec * 1.2);
  }

  // Human-readable line-rate label for the copy modal: "MIDI's
  // 31250 baud" vs "RS-232 at 50000 baud". Read off the live
  // connection so it always reflects what's actually about to
  // transfer.
  $: lineRateLabel = (() => {
    const s = $connectionStatus;
    if (s.connected && s.kind === 'serial' && s.baud) return `RS-232 at ${s.baud} baud`;
    return `MIDI's 31250 baud`;
  })();

  function startCopyTicker() {
    stopCopyTicker(); // safety: cancel any prior interval
    copyElapsedMs = 0;
    copyTickHandle = setInterval(() => {
      copyElapsedMs = Date.now() - copyStartMs;
    }, 100);
  }

  function stopCopyTicker() {
    if (copyTickHandle !== null) {
      clearInterval(copyTickHandle);
      copyTickHandle = null;
    }
  }

  async function copyFromDevice() {
    if (!smp || smp.source !== 'device') return; // guard: button is disabled, but be defensive
    if (copyPhase === 'copying') return;          // ignore double-clicks
    const slot = smp.slot;
    const name = smp.name;
    copyError = '';
    copyPhase = 'copying';
    copyStartMs = Date.now();
    // Refresh SPRM first so smp.length carries the device's real
    // word count, not the catalog's skinny placeholder (`1`). The
    // progress bar's time estimate scales linearly with length —
    // without this, a 164K-word sample would show "est. 1s" and
    // the bar would slam to 99% inside a tick.
    try {
      await ensureSampleLoaded(slot, true);
    } catch (e: any) {
      copyError = 'Failed to read sample parameters: ' + String(e?.message ?? e);
      copyPhase = 'error';
      return;
    }
    startCopyTicker();
    try {
      const copied = (await (App as any).CopySampleAudio(slot)) as { words?: number[]; sampleRateHz?: number } | null;
      const words = copied?.words ?? [];
      if (!Array.isArray(words) || words.length === 0) {
        throw new Error('Device returned an empty sample');
      }
      // Attach to the live samples store. Re-derives PCM from the
      // 12-bit words so the waveform display flips from synthetic
      // to real on the next paint. Take the dump header's rate as
      // authoritative for these bytes — it can diverge from the
      // SPRM (e.g. when the device internally resampled a too-fast
      // upload but left the SPRM rate alone).
      const dumpRate = copied?.sampleRateHz;
      const audio: { words12: number[]; pcm: number[]; rate?: number } = {
        words12: words,
        pcm: wordsToPcm(words),
      };
      if (dumpRate) audio.rate = dumpRate;
      samples.update((xs) =>
        xs.map((s) => (s.slot === slot ? { ...s, ...audio } : s)),
      );
      // Persist to the disk cache so the next session re-attaches
      // without another round-trip. Uses the same path as Apply
      // Slicing — keyed by words.length (the cache truth, not the
      // sample's possibly-stale length field). Rate falls back to
      // the SPRM rate (never 0 — a zero rate in the cache would
      // resurface as division-by-zero duration math on hydrate).
      try {
        await (App as any).PutCachedWaveform(slot, name, words.length, words, dumpRate || smp.rate);
      } catch {}
      copyPhase = 'done';
    } catch (e: any) {
      copyError = String(e?.message ?? e);
      copyPhase = 'error';
    } finally {
      stopCopyTicker();
    }
  }

  function closeCopyModal() {
    copyPhase = 'idle';
    copyError = '';
    stopCopyTicker();
  }

  // ---------- Send to S950 ----------
  // Per-sample upload of audio + SPRM. Two button paths land here:
  // (1) "Send to S950" for local samples — pushes SDATA + SPRM and
  //     flips the row to source='device' on success.
  // (2) "Send SPRM to S950" for device samples — params only, no
  //     audio re-upload. Same flow as (1) but skips the audio.
  // Phase events come from the backend's "sendsample:progress"
  // Wails channel; the modal subscribes for a live phase label.
  type SendPhase = 'idle' | 'sending' | 'done' | 'error';
  let sendPhase: SendPhase = 'idle';
  let sendError = '';
  let sendStatus = '';  // current "phase / message" from backend events
  let sendKind: 'full' | 'sprm' = 'full';  // distinguishes the two flows in the modal title
  let sendUnsub: (() => void) | null = null;
  // Synthetic progress timing — same pattern as the Copy modal. The
  // Wails Send path is open-loop (the OS queues the whole envelope
  // in one shot), so we can't drive a real progress bar from a byte
  // counter. Elapsed / estimated gives the user "yes, still working"
  // feedback. SPRM-only sends finish in <1s and skip the bar.
  let sendStartMs = 0;
  let sendElapsedMs = 0;
  let sendTickHandle: ReturnType<typeof setInterval> | null = null;

  // sendPercent mirrors copyPercent. Estimated time is dominated by
  // the SDATA dump (the SPRM write is ~150 bytes — sub-1s), so we
  // use the same wire-rate estimator as Copy. Returns 0 for the
  // SPRM-only path so the bar doesn't appear in that modal flow.
  $: sendPercent = (() => {
    if (sendPhase === 'done')  return 100;
    if (sendPhase === 'error') return 0;
    if (sendPhase !== 'sending' || !smp || sendKind !== 'full') return 0;
    const estMs = estimatedCopySeconds(smp.length) * 1000;
    if (estMs <= 0) return 0;
    const pct = (sendElapsedMs / estMs) * 100;
    if (pct >= 95) return Math.min(99, 95 + (pct - 95) / 5);
    return Math.min(99, Math.max(0, pct));
  })();

  function startSendTicker() {
    stopSendTicker();
    sendElapsedMs = 0;
    sendTickHandle = setInterval(() => {
      sendElapsedMs = Date.now() - sendStartMs;
    }, 100);
  }
  function stopSendTicker() {
    if (sendTickHandle !== null) {
      clearInterval(sendTickHandle);
      sendTickHandle = null;
    }
  }

  function startSendListener() {
    if (sendUnsub) return;
    sendUnsub = EventsOn('sendsample:progress', (p: any) => {
      sendStatus = p?.message ?? '';
    });
  }
  function stopSendListener() {
    if (sendUnsub) { sendUnsub(); sendUnsub = null; }
  }

  async function sendToDevice() {
    if (!smp) return;
    if (sendPhase === 'sending') return;
    const slot = smp.slot;
    if (!smp.words12 || smp.words12.length === 0) {
      sendError = 'No host-side audio to upload. Re-import the sample or Copy from S950.';
      sendPhase = 'error';
      sendKind = 'full';
      return;
    }
    sendPhase = 'sending';
    sendKind = 'full';
    sendError = '';
    sendStatus = `Preparing upload to slot ${slot}…`;
    sendStartMs = Date.now();
    startSendTicker();
    startSendListener();
    try {
      const params = sampleToSampleParams(smp);
      await (App as any).SendSample(slot, smp.words12, smp.rate, params);
      // Promote the row from local to device — it's on the S950 now
      // and Send-to-S950 would otherwise reappear after the upload
      // succeeded. Also clear deviceSnapshot: the local state IS the
      // device state now, so any pre-edit snapshot is stale (and a
      // subsequent edit should snapshot the just-uploaded state, not
      // some older one).
      samples.update((xs) => xs.map((s) => (s.slot === slot ? { ...s, source: 'device', deviceSnapshot: undefined } : s)));
      sendPhase = 'done';
      setSync('synced', 'Synced');
      // Background: persist this sample's audio to the disk cache
      // now that we know what name it has on the device. Future
      // sessions hydrate without a re-fetch.
      void persistSamplesToCache([slot]);
      // Re-scan device memory so the topbar chip reflects the
      // newly-added words. Cheap (one SPRM per device sample) and
      // matches what Apply Slicing does on completion.
      void scanMemory();
    } catch (e: any) {
      sendError = String(e?.message ?? e);
      sendPhase = 'error';
    } finally {
      stopSendListener();
      stopSendTicker();
    }
  }

  async function sendSPRMToDevice() {
    if (!smp) return;
    if (sendPhase === 'sending') return;
    const slot = smp.slot;
    sendPhase = 'sending';
    sendKind = 'sprm';
    sendError = '';
    sendStatus = `Writing parameters to slot ${slot}…`;
    try {
      const params = sampleToSampleParams(smp);
      await (App as any).SetSampleParams(slot, params);
      sendPhase = 'done';
      setSync('synced', 'Synced');
    } catch (e: any) {
      sendError = String(e?.message ?? e);
      sendPhase = 'error';
    }
  }

  function closeSendModal() {
    sendPhase = 'idle';
    sendError = '';
    sendStatus = '';
    stopSendTicker();
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

    // Capture host audio per destination slot BEFORE the wire round
    // trip. refreshCatalog will replace the local rows with fresh
    // device-sourced ones that have no PCM/words12 — we re-attach
    // the captured audio after the refresh so the waveform display
    // can render the real signal instead of the synthetic squiggle.
    // See slicing.ts captureSliceAudio docstring for details.
    const sourceSlot = get(selectedSampleSlot);
    const destSlots: number[] = Array.isArray(slicePreflight?.sampleSlots)
      ? slicePreflight.sampleSlots
      : [];
    let capturedAudio: Map<number, CapturedAudio> = new Map();
    if (hasCommitted) {
      // Committed children already hold per-slice audio (commitSlices
      // extracted it earlier); zip directly with destSlots rather
      // than re-slicing a concat'd buffer.
      committedChildren.forEach((c, i) => {
        if (i < destSlots.length) {
          capturedAudio.set(destSlots[i], {
            pcm: c.pcm,
            words12: c.words12 ?? [],
          });
        }
      });
    } else {
      capturedAudio = captureSliceAudio(
        { words12: smp.words12, pcm: smp.pcm },
        $slicing.slices.map((sl) => ({ start: sl.start, length: sl.length })),
        destSlots,
      );
    }

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
        // Restore host audio on the freshly-loaded device rows so
        // waveform display + future slice-loop placement gets the
        // real signal instead of a synthetic envelope.
        if (capturedAudio.size > 0) {
          samples.update((xs) => attachAudioToSamples(xs, capturedAudio));
        }
        try { await scanMemory(); } catch {}
        // Phase 1B: persist host audio to the on-disk cache so the
        // next session can re-attach without a re-upload. MUST run
        // after scanMemory: refreshCatalog reset every row to a
        // skinny stub (rate 26040, length 1) and the cache entry
        // now carries the rate — persisting before the SPRM scan
        // would freeze the stub rate into the cache, and the next
        // session's hydration would overwrite the real SPRM rate
        // with it (wrong preview pitch, and a subsequent SPRM
        // live-sync would even write the bogus rate to the device).
        // Fire-and-forget — a cache write failure degrades to the
        // pre-1B world (synthetic waveform on relaunch), not a
        // crash or partial state.
        if (capturedAudio.size > 0) {
          void persistSamplesToCache(Array.from(capturedAudio.keys()));
        }
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
    // Stop the Copy-from-S950 progress ticker if it's running so
    // setInterval doesn't keep firing after the route unmounts.
    stopCopyTicker();
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
  // buildWaveformPaths already produces a path spanning x=0..1000 for
  // the visible window only — so the SVG viewBox is a fixed 0 0 1000 200.
  // Earlier code also cropped the viewBox to (viewStart, visibleN) of
  // the full sample, which double-zoomed the waveform relative to the
  // marker overlay (markers use pct() in visible-% space; the waveform
  // ended up in a different word→pixel mapping) and made markers drift
  // off their target words as zoom changed.

  // Word → visible-% mapping. Off-screen markers return out-of-range
  // values; CSS overflow: hidden on the waveform clips them so we
  // don't need to filter in the template.
  function pct(word: number): number {
    if (visibleN <= 0) return 0;
    return ((word - viewStart) / visibleN) * 100;
  }

  // Marker / region positions in visible-% space. The loop region
  // is anchored at Start (SPRM has no separate LoopStart) and runs
  // for LoopLength words.
  $: pctStart     = pct(smp.start);
  $: pctEnd       = pct(smp.end);
  $: pctLoopWidth = (smp.loopLength / visibleN) * 100;

  // Playhead position (visible-% space). The store carries a slot
  // id so the cursor stays anchored to its origin sample even if
  // the user clicks to a different row mid-playback — null when
  // playback isn't live or the active sample isn't on screen.
  $: playheadPct = $playhead.active && $playhead.sampleSlot === smp.slot
    ? pct($playhead.currentWord)
    : null;

  $: durationSec = smp.length / smp.rate;

  function onNameInput(e: Event) {
    selectedSample.update({ name: (e.target as HTMLInputElement).value });
  }

  // ---------- Marker drag on the waveform ----------
  // Each marker grabs onto a CSS variable on .waveform. Drag converts
  // pixel x within the strip to a word index (length-relative), then
  // writes to the store with appropriate clamping.
  // No loopStart marker — the SPRM block has no LoopStart field, so
  // the loop is always anchored at Start. Dragging the Start handle
  // moves the loop's origin; the loopEnd handle adjusts LoopLength.
  type MarkerKind = 'start' | 'end' | 'loopEnd';
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
    } else if (activeMarker === 'loopEnd') {
      const newLength = Math.max(0, Math.min(word, smp.length) - smp.start);
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

  // Whole-sample preview from the identity strip's ▶ button.
  //   host   → Web Audio playback of the [Start..End] window
  //            honouring SPRM replay mode + loop.
  //   device → MIDI Note On to the S950 (held for the sample's
  //            audible duration). Audible only if a program maps
  //            PREVIEW_NOTE (C3) to this sample. Slice play
  //            buttons stay host-only — uncommitted slices have
  //            no device-side mapping to trigger.
  function previewWhole() {
    if ($previewMode === 'device') {
      void previewWholeViaMidi();
    } else {
      // Best-of-both-worlds host preview:
      //   • Local samples → recorded pitch (offset 0). The user
      //     hears what they imported.
      //   • Device samples mapped to a SINGLE key → mirror the
      //     device's pitch shift so host preview sounds like what
      //     the S950 will play when this program is loaded.
      //   • Device samples on ranged keygroups or without a
      //     mapping → recorded pitch (a ranged keygroup has no
      //     single device pitch to mirror).
      // The pure helper in preview.ts owns the rule so this stays
      // testable + a single-source-of-truth.
      const offset = preview.effectivePreviewOffset({
        source: smp.source,
        mappingNote: deviceMapping?.note,
        mappingLowKey: deviceMapping?.lowKey,
        mappingHighKey: deviceMapping?.highKey,
        mappingConstPitch: deviceMapping?.constPitch,
        mappingTranspose: deviceMapping?.transpose,
      });
      preview.previewSample(smp, offset);
    }
  }
  // deviceMapping locates a loaded program / keygroup that
  // references the current sample by name, so MIDI preview can
  // trigger the right note (instead of always sending C3 and
  // hoping a kit happens to map it). null = no mapping found;
  // the user will hear silence on Device-mode preview.
  type DeviceMapping = {
    programSlot: number;
    programName: string;
    keygroupN: number;
    note: number;
    lowKey: number;
    highKey: number;
    constPitch: boolean;
    transpose: number;
  };
  $: deviceMapping = ((): DeviceMapping | null => {
    if (!smp || smp.source !== 'device') return null;
    const targetName = smp.name.trim();
    if (!targetName) return null;
    for (const p of $programs) {
      for (const k of p.keygroups) {
        const softMatch = k.soft.sample.trim() === targetName;
        if (softMatch || k.loud.sample.trim() === targetName) {
          return {
            programSlot: p.slot,
            programName: p.name || `slot ${p.slot}`,
            keygroupN: k.n,
            // Use the keygroup's centre key — most kits map a
            // sample to a single key, in which case low == high.
            // For ranged keygroups, the middle is the least-
            // surprising MIDI trigger note. The raw range +
            // const-pitch flag are carried alongside so
            // effectivePreviewOffset can tell pitch-tracking
            // single-key maps (apply pitch offset) apart from
            // ranged keygroups and constant-pitch slice kits
            // (no device transpose — host preview stays at
            // recorded pitch).
            note: Math.floor((k.lowKey + k.highKey) / 2),
            lowKey: k.lowKey,
            highKey: k.highKey,
            constPitch: k.constPitch,
            // The matched layer's per-keygroup transpose — part of
            // the device's pitch math alongside key tracking. Use
            // whichever layer the name matched on.
            transpose: softMatch ? k.soft.transpose : k.loud.transpose,
          };
        }
      }
    }
    return null;
  })();

  // midiPreviewing toggles the play button's "live" styling for
  // the MIDI mode — the host-side playhead store doesn't fire when
  // audio is coming from the device, so we keep a local mirror.
  let midiPreviewing = false;
  async function previewWholeViaMidi() {
    if (!smp) return;
    // Hold for the sample's audible window (Start..End) at its
    // natural rate. Add a small tail so the device's own release
    // stage doesn't get clipped. Caps at PreviewMaxDuration on
    // the backend so a runaway value can't lock the wire.
    const playableWords = Math.max(1, (smp.end || smp.length) - smp.start);
    const durationMs = Math.ceil((playableWords / smp.rate) * 1000) + 100;
    // Use the discovered keygroup mapping's note when one exists,
    // otherwise fall back to C3. Without a mapping the device will
    // be silent (no keygroup → no audio); we still send the note
    // so the user sees TX traffic in the wire log and isn't left
    // wondering whether the call dispatched.
    const note = deviceMapping?.note ?? PREVIEW_NOTE;
    midiPreviewing = true;
    try {
      await (App as any).PreviewMidi(
        note,
        PREVIEW_VELOCITY,
        $connectionStatus.channel ?? 0,
        durationMs,
      );
    } catch (e) {
      console.warn('PreviewMidi failed:', e);
    } finally {
      midiPreviewing = false;
    }
  }

  // Reactive "is the current sample being previewed right now?"
  // Drives the play button's active styling. Host mode reads the
  // shared playhead store (already maintained by preview.ts) so
  // the button highlights whether playback started from this
  // button OR the spacebar. MIDI mode reads the local mirror.
  $: isPreviewing = midiPreviewing
    || ($playhead.active && $playhead.sampleSlot === smp.slot);

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
    addSliceAt(maybeSnapToZero(word));
  }

  // Snap a word index to the nearest zero crossing when the slicing
  // panel's "snap to zero" toggle is on. Prefers the higher-resolution
  // int16 PCM when present (imported samples); falls back to the
  // 12-bit S950 word buffer (audio captured from the device). Both
  // are optional: when neither is loaded (device sample without a
  // pulled audio buffer), the snap is a no-op. Radius capped at
  // ~38ms of audio (1000 words @ 26 kHz) so a click in a silent
  // run doesn't snap halfway across the sample.
  function maybeSnapToZero(word: number): number {
    if (!$slicing.snapToZero) return word;
    const radius = Math.min(1000, Math.floor(smp.length * 0.05));
    if (smp.pcm && smp.pcm.length > 1) {
      return findZeroCrossing(smp.pcm, word, radius, 0);
    }
    if (smp.words12 && smp.words12.length > 1) {
      return findZeroCrossing(smp.words12, word, radius, 2048);
    }
    return word;
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
    // Snap the drop position when the toggle is on. Snapping
    // mid-drag means the slice tracks the mouse but jumps to the
    // nearest crossing — same UX as DAWs that snap-while-dragging.
    next = maybeSnapToZero(next);

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

  // ---------- Horizontal scrollbar (zoomed-in pan) ----------
  // Shown when zoom > 1 (visible window is narrower than the sample).
  // Track spans the full sample [0..1]; thumb spans the visible window
  // [viewStart/length .. (viewStart+visibleN)/length]. Drag the thumb
  // to pan; clicking the empty track centers the view on that point.
  // Mirrors the pattern used in the related PB950 Waveform component.
  let sbTrackEl: HTMLDivElement | undefined;
  let sbDragging = false;
  let sbStartX = 0;
  let sbStartCenter = 0;
  function onScrollThumbDown(e: PointerEvent) {
    if (e.button !== 0) return;
    e.preventDefault();
    e.stopPropagation(); // thumb grab must not double-fire the track-click
    sbDragging = true;
    sbStartX = e.clientX;
    sbStartCenter = $slicing.zoomCenter;
    (e.target as Element).setPointerCapture?.(e.pointerId);
  }
  function onScrollThumbMove(e: PointerEvent) {
    if (!sbDragging || !sbTrackEl) return;
    const trackW = sbTrackEl.getBoundingClientRect().width || 1;
    // dx/trackW is a delta in the same normalized [0..1] space the
    // zoomCenter lives in (track == full sample). viewStart's clamp
    // in the reactive derivation handles edge cases where the new
    // center would push the visible window off either end.
    const dxNorm = (e.clientX - sbStartX) / trackW;
    updateSlicing({
      zoomCenter: Math.max(0, Math.min(1, sbStartCenter + dxNorm)),
    });
  }
  function onScrollThumbUp() { sbDragging = false; }
  function onScrollTrackDown(e: PointerEvent) {
    if (e.button !== 0 || !sbTrackEl) return;
    // Click the empty track → center the view on that point.
    const r = sbTrackEl.getBoundingClientRect();
    const clickNorm = (e.clientX - r.left) / (r.width || 1);
    updateSlicing({
      zoomCenter: Math.max(0, Math.min(1, clickNorm)),
    });
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
            {#if s.source === 'local' && s.originalSource === 'device'}
              <!-- Device-originated sample that's been edited locally
                   (resampled, retuned, etc.). Click the tag to revert:
                   re-pull SPRM from device + drop host PCM. No ×
                   button — revert is the way to "remove" the local
                   divergence; the device row stays. -->
              <button
                type="button"
                class="sample__tag sample__tag--edited"
                title="Local edits diverged from S950 · click to revert (re-pull SPRM, drop host audio)"
                on:click|stopPropagation={() => revertSample(s.slot)}>↺ EDITED</button>
            {:else if s.source === 'local'}
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
          {#if smp.pcm && smp.pcm.length > 0}
            <!-- Resample available for any sample with host PCM —
                 imports AND Copy-from-S950'd device samples both
                 qualify. The dropdown groups standard rates and
                 the lo-fi presets so the user sees both the "I
                 want a clean conversion" and "I want SP1200
                 character" options without leaving the cell.
                 Resampling a device sample diverges its host PCM
                 from the device's stored audio; the onRateChange
                 handler flips source to 'local' so the user gets
                 the Send-to-S950 button to re-sync. -->
            <label class="chip chip--select identity__rate-select" title={resampleInFlight ? 'Resampling…' : 'Resample host audio before Send to S950'}>
              <span class="chip__value">{resampleInFlight ? 'Resampling…' : `${(smp.rate / 1000).toFixed(2)} kHz`}</span>
              <select value={smp.rate} on:change={onRateChange} disabled={resampleInFlight}>
                <option value={smp.rate}>{(smp.rate / 1000).toFixed(2)} kHz (current)</option>
                <optgroup label="Lo-Fi">
                  {#each RATE_LOFI_PRESETS as p (p.rate)}
                    {#if p.rate !== smp.rate}
                      <option value={p.rate}>{p.label}</option>
                    {/if}
                  {/each}
                </optgroup>
                <optgroup label="Standard">
                  {#each RATE_STANDARD_PRESETS as r (r)}
                    {#if r !== smp.rate}
                      <option value={r}>{(r / 1000).toFixed(2)} kHz</option>
                    {/if}
                  {/each}
                </optgroup>
              </select>
            </label>
          {:else}
            <span class="field">{(smp.rate / 1000).toFixed(2)} kHz</span>
          {/if}
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
          <div class="preview-row">
            <button
              type="button"
              class="identity__play"
              class:identity__play--active={isPreviewing}
              on:click={previewWhole}
              disabled={$previewMode === 'host'
                ? !preview.hasHostAudio(smp)
                : !$connectionStatus.connected}
              title={$previewMode === 'host'
                ? (preview.hasHostAudio(smp)
                    ? 'Play (Start → End) · spacebar'
                    : 'Import .wav / .aiff to enable host-side preview')
                : !$connectionStatus.connected
                  ? 'Connect to the S950 to trigger MIDI preview'
                  : deviceMapping
                    ? `Trigger MIDI note ${deviceMapping.note} on channel ${$connectionStatus.channel} — mapped by program "${deviceMapping.programName}" (kg ${deviceMapping.keygroupN})`
                    : 'No loaded program maps this sample — the device will be silent. Switch to Host preview or upload + map this sample to a keygroup first.'}
              aria-label="Preview sample">▶</button>
            <!-- Preview routing — Host plays via Web Audio (instant),
                 Device sends a MIDI Note On to the S950. Choice is
                 global (per-session) so flipping between samples
                 doesn't change mode. -->
            <label class="chip chip--select preview-source" title="Where to send preview audio">
              <span class="chip__value">{$previewMode === 'device' ? 'Device' : 'Host'}</span>
              <select bind:value={$previewMode}>
                <option value="host">Host (Web Audio)</option>
                <option value="device">Device (MIDI trigger)</option>
              </select>
            </label>
          </div>
          <!-- Device-mode mapping affordance. MIDI preview is only
               audible when a loaded program maps this sample to a
               note; we surface the discovered mapping (or its
               absence) so the user sees what'll happen before
               clicking play. Hidden in Host mode (irrelevant). -->
          {#if $previewMode === 'device'}
            {#if deviceMapping}
              <div class="preview-hint preview-hint--ok">
                via <strong>{deviceMapping.programName}</strong> · kg {deviceMapping.keygroupN} · note {deviceMapping.note}
              </div>
            {:else}
              <div class="preview-hint preview-hint--warn">
                No loaded program maps this sample — device will be silent.
              </div>
            {/if}
          {/if}
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
              on:change={(e) => setBeats({ tempo: e.detail })} />
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
          <span class="slice-beats__count">
            → {slicesFor($slicing.bars, $slicing.division)} slices
            {#if smp.rate > 0}
              · expected {expectedSeconds($slicing.bars, $slicing.division, $slicing.tempo).toFixed(2)}s
              · actual {(smp.length / smp.rate).toFixed(2)}s
            {/if}
          </span>
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
        class:is-zoomed={$slicing.zoom > 1}
        bind:this={waveformEl}
        on:click={onWaveformClick}
        on:mousemove={onWaveformMouseMove}
        on:mouseleave={onWaveformMouseLeave}
        on:wheel|preventDefault|nonpassive={onWaveformWheel}
        style="--start: {pctStart}%; --end: {pctEnd}%; --loop-start: {pctStart}%; --loop-width: {pctLoopWidth}%;">
        <!-- Mirror horizontally when the sample's Reversed SPRM flag
             is on — the squiggle's shape is still synthetic, but the
             direction it plays back IS real. -->
        <svg
          class="waveform__svg"
          class:waveform__svg--reversed={smp.reverse}
          viewBox="0 0 1000 200"
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
          <!-- No separate loop-start marker — SPRM has only Start +
               LoopLength (no LoopStart field). The loop region begins
               at the sample's Start handle; the loop-end handle below
               adjusts LoopLength. -->
          <div class="marker marker--loop-end" on:mousedown={(e) => beginMarkerDrag(e, 'loopEnd')}>
            <span class="marker__handle marker__handle--right" on:mousedown={(e) => beginMarkerDrag(e, 'loopEnd')}>Loop · {(smp.start + smp.loopLength).toLocaleString()}</span>
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

          <!-- Playback playhead. Only renders while the preview
               source is actually running for THIS sample row (the
               store tracks slot identity), and clips off-screen
               via the overlay's overflow:hidden when zoom puts
               the position outside the visible window. -->
          {#if playheadPct !== null}
            <div class="waveform__playhead" style="left: {playheadPct}%;"></div>
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
        <!-- Horizontal scrollbar — only when zoomed in. Track spans
             the whole sample [0..1]; thumb spans the visible window.
             Click the empty track to centre the view on that point;
             drag the thumb to pan. Sits above the ruler. -->
        {#if $slicing.zoom > 1 && smp.length > 0}
          <div
            class="waveform__scroll"
            bind:this={sbTrackEl}
            on:pointerdown={onScrollTrackDown}>
            <div
              class="waveform__scroll-thumb"
              style="left: {(viewStart / smp.length) * 100}%; width: {(visibleN / smp.length) * 100}%;"
              on:pointerdown={onScrollThumbDown}
              on:pointermove={onScrollThumbMove}
              on:pointerup={onScrollThumbUp}
              on:pointercancel={onScrollThumbUp}
              title="drag to scroll · click track to recentre"></div>
          </div>
        {/if}
      </div>
      <div class="waveform__legend">
        <!-- Three waveform states the user needs to tell apart:
             1. device sample with host audio (Phase 1A/1B/1C):
                the waveform is real AND the device has identical
                bytes — host and S950 are in sync. Tag flips to a
                green "✓ synced" badge to make this explicit.
             2. local sample with host audio: real waveform, but
                only on the host — the S950 doesn't have these
                bytes yet (Apply Slicing pushes them to the device).
             3. device sample without host audio: synthetic
                squiggle, the host doesn't have the bytes (run
                Copy from S950 to fetch them). -->
        {#if smp.pcm && smp.pcm.length > 0}
          {#if smp.source === 'device'}
            <span class="waveform__preview-tag waveform__preview-tag--synced">
              <span class="sync-dot" aria-hidden="true"></span>
              waveform · synced with S950
            </span>
          {:else}
            <span class="waveform__preview-tag">
              waveform · imported audio (local only)
            </span>
          {/if}
        {:else}
          <span class="waveform__preview-tag">
            preview · synthetic waveform
          </span>
        {/if}
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
          <div class="row" title="Loop length in words. The loop runs from Start for this many words; SPRM has no separate loop-start field.">
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
               so all three toggles fit at narrow column widths.
               Unlike Reverse, Mode IS honored by the device's playback
               engine on SPRM write (checked at end-of-region per
               trigger), so no front-panel ENT is required. -->
          <div class="row">
            <span class="row__label">Mode</span>
            <div class="mode-row">
              <button type="button" class="toggle {smp.mode === 'one-shot' ? 'on' : ''}" on:click={() => selectedSample.update({ mode: 'one-shot' })}>Once</button>
              <button type="button" class="toggle {smp.mode === 'loop' ? 'on' : ''}"     on:click={() => selectedSample.update({ mode: 'loop' })}>Loop</button>
              <button type="button" class="toggle {smp.mode === 'ping-pong' ? 'on' : ''}" on:click={() => selectedSample.update({ mode: 'ping-pong' })}>Ping-pong</button>
            </div>
          </div>
          {#if smp.source === 'local'}
            <!-- Reverse is hidden for device samples — toggling the
                 SPRM byte updates stored memory but doesn't refresh
                 the device's DMA direction cache, so the toggle
                 would be misleadingly inert. For local samples it
                 works end-to-end: host preview reverses immediately
                 and the SPRM is read fresh on the device's first
                 selection after Send-to-S950. See
                 project-sprm-cache-limitation memory. -->
            <div class="row">
              <span class="row__label">Reverse</span>
              <button
                type="button"
                class="toggle {smp.reverse ? 'on' : ''}"
                on:click={() => selectedSample.update({ reverse: !smp.reverse })}>
                {smp.reverse ? 'On' : 'Off'}
              </button>
            </div>
          {:else}
            <!-- Device-sample-only footnote. Stays low-key (italic,
                 muted) since it's informational, not actionable.
                 Names only the operations we genuinely can't do
                 from the GUI: timestretch (no host equivalent yet),
                 reverse (device-cache limitation). Resample is
                 omitted on purpose — the Rate dropdown above already
                 handles it via re-upload. -->
            <div class="row__hint">
              <em>Reverse + timestretch are front-panel-only on the S950
                for samples currently on the device. Resample is
                available above — it transforms host audio and re-
                uploads.</em>
            </div>
          {/if}
          <div class="row">
            <span class="row__label">Vel x-fade</span>
            <button type="button" class="toggle {smp.velXfade ? 'on' : ''}" on:click={() => selectedSample.update({ velXfade: !smp.velXfade })}>
              {smp.velXfade ? 'On' : 'Off'}
            </button>
          </div>

          <hr class="panel__divider" />

          <div class="row">
            <span class="row__label">Tune</span>
            <!-- step=1 (semitones) so wheel + drag + +/- buttons all
                 move in musically-meaningful increments. The S950's
                 protocol stores tune as int16 1/16-semitone units;
                 integer semitones round-trip exactly. Click-to-type
                 still accepts fractional values for sub-semitone
                 cents tuning when needed. -->
            <NumField
              value={smp.tune}
              min={-50} max={50} step={1}
              format={(v) => `${v >= 0 ? '+' : ''}${v.toFixed(2)}`}
              on:change={(e) => selectedSample.update({ tune: e.detail })} />
          </div>
          <div class="row">
            <span class="row__label">Loudness</span>
            <NumField
              value={smp.loudness}
              min={-50} max={50} step={1}
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
                  <span class="slice-list__range" title="{sl.start.toLocaleString()} — {(sl.start + sl.length).toLocaleString()}">{sl.start.toLocaleString()} — {(sl.start + sl.length).toLocaleString()}</span>
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
              <div class="row">
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
          <div class="card__subtitle">{connectionLabel} · slot {smp.slot.toString().padStart(2, '0')}</div>
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
          <!-- Copy from S950 (Phase 1C) is only viable over RS-232 —
               the MIDI path's pre-emptive ACK pump corrupts large
               samples mid-stream. We hide the button entirely on
               non-serial sessions so a disabled-with-tooltip state
               can't confuse users into thinking it's broken; if you
               want it back, switch the device to RS-232C on the
               front panel and reconnect over the serial cable. -->
          {#if $transportKind === 'serial'}
            <button
              type="button"
              class="btn"
              disabled={$imgMode || smp.source === 'local' || copyPhase === 'copying'}
              title={$imgMode
                ? 'IMG editor mode — device connection is disabled'
                : smp.source === 'local'
                ? 'Select an on-device sample to copy its audio from the S950'
                : 'Pull SDATA for this slot from the S950 and cache it on disk'}
              on:click={copyFromDevice}>Copy from S950</button>
          {/if}
          <button
            type="button"
            class="btn"
            disabled={!smp.pcm || smp.pcm.length === 0 || saveWavBusy}
            title={!smp.pcm || smp.pcm.length === 0
              ? 'No host audio to save. Copy from S950 first, or import a .wav.'
              : 'Save the current host PCM to disk as a 16-bit mono .wav'}
            on:click={saveWav}>Save .wav...</button>
          <hr class="panel__divider" />
          <!-- Context-aware Send. Local samples get the full "audio
               + SPRM" upload (primary); device samples already have
               audio on the device, so the only meaningful change is
               the SPRM block (parameters). One button visible at a
               time matches what the user can actually do for the
               row's state. -->
          {#if smp.source === 'local'}
            <button
              type="button"
              class="btn btn--primary"
              disabled={$imgMode || !smp.words12 || smp.words12.length === 0 || sendPhase === 'sending'}
              title={$imgMode
                ? 'IMG editor mode — device connection is disabled'
                : !smp.words12 || smp.words12.length === 0
                ? 'No host-side audio to upload — re-import the sample'
                : 'Upload audio + SPRM to the chosen slot on the S950'}
              on:click={sendToDevice}>Send to S950</button>
          {:else}
            <button
              type="button"
              class="btn"
              disabled={$imgMode || sendPhase === 'sending'}
              title={$imgMode
                ? 'IMG editor mode — device connection is disabled'
                : 'Write the edited parameters back to this slot\'s SPRM block (audio unchanged)'}
              on:click={sendSPRMToDevice}>Send SPRM to S950</button>
          {/if}
          <button
            type="button"
            class="btn"
            disabled={$imgMode}
            title={$imgMode ? 'IMG editor mode — device connection is disabled' : undefined}
            on:click={getSPRMFromDevice}>Get SPRM from S950</button>
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
        <div class="modal__route">{$slicing.slices.length} slices · {connectionLabel}</div>
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

<!-- Phase 1C "Copy from S950" modal.
     States:
       copying  → indeterminate progress with ETA (rtmidi can't surface
                  mid-stream byte counts, so it's a spinner + a copy of
                  the host's wire-rate estimate)
       done     → success terminal, "Close" returns to idle
       error    → error terminal, "Close" returns to idle -->
{#if copyPhase !== 'idle'}
  <div class="modal-backdrop is-open" role="dialog" aria-modal="true">
    <div class="modal">
      <header class="modal__head">
        <h2 class="modal__title">
          {#if copyPhase === 'copying'}Copying from S950 — <strong>{smp.name}</strong>
          {:else if copyPhase === 'done'}Copied — <strong>{smp.name}</strong>
          {:else}Copy error
          {/if}
        </h2>
        <div class="modal__route">slot {smp.slot.toString().padStart(2, '0')} · {connectionLabel}</div>
      </header>
      <div class="modal__body">
        {#if copyPhase === 'copying'}
          <div class="modal__step">
            Streaming {smp.length.toLocaleString()} words at {lineRateLabel} line rate
            (est. {Math.floor(estimatedCopySeconds(smp.length) / 60)}m {estimatedCopySeconds(smp.length) % 60}s).
          </div>
          <!-- Synthetic progress bar — ticks against the estimated
               transfer time, capped at 99% until the call returns.
               rtmidi only surfaces complete SysEx envelopes so we
               can't drive this from received-byte counts; the
               elapsed-vs-estimated heuristic is the best we can do
               without losing the visual cue that something IS
               happening on the wire. -->
          <div class="progress">
            <div class="progress__bar" style="width: {copyPercent}%;"></div>
            <div class="progress__label">
              {Math.floor(copyPercent)}% · {Math.max(0, Math.round(copyElapsedMs / 1000))}s
              / ~{estimatedCopySeconds(smp.length)}s
            </div>
          </div>
          <div class="modal__hint">
            Progress is an estimate based on wire-rate + ACK-pump overhead, not actual byte
            counts. Don't close this window — the transfer is still running.
          </div>
        {:else if copyPhase === 'done'}
          <div class="modal__step">
            ✓ Copied {smp.length.toLocaleString()} words from slot {smp.slot} in
            {Math.max(1, Math.round((Date.now() - copyStartMs) / 1000))}s.
          </div>
          <div class="progress">
            <div class="progress__bar" style="width: 100%;"></div>
            <div class="progress__label">100%</div>
          </div>
          <div class="modal__hint">
            The waveform is now backed by real audio + persisted to the on-disk cache.
            Future sessions will re-attach without another round-trip.
          </div>
        {:else}
          <div class="modal__step modal__step--error">{copyError}</div>
        {/if}
      </div>
      <footer class="modal__foot">
        {#if copyPhase === 'copying'}
          <span class="modal__waitnote">do not close · sample transfer in progress</span>
        {:else}
          <button type="button" class="btn btn--primary" on:click={closeCopyModal}>Close</button>
        {/if}
      </footer>
    </div>
  </div>
{/if}

<!-- Send-to-S950 modal. Three phases: sending (live status from
     backend events), done (✓ summary), error (close). Full upload
     (sendKind='full') and SPRM-only (sendKind='sprm') share the
     modal — title differs, body content is the same shape. The
     wire-time portion is open-loop (PutSampleOpenLoop queues the
     full envelope to the OS in one shot), so we surface the
     backend's phase label instead of a synthetic progress bar:
     "Uploading…", "Draining…", "Writing parameters…". -->
{#if sendPhase !== 'idle'}
  <div class="modal-backdrop is-open" role="dialog" aria-modal="true">
    <div class="modal">
      <header class="modal__head">
        <h2 class="modal__title">
          {#if sendPhase === 'sending' && sendKind === 'full'}Sending to S950 — <strong>{smp.name}</strong>
          {:else if sendPhase === 'sending'}Writing SPRM — <strong>{smp.name}</strong>
          {:else if sendPhase === 'done' && sendKind === 'full'}Uploaded — <strong>{smp.name}</strong>
          {:else if sendPhase === 'done'}SPRM written — <strong>{smp.name}</strong>
          {:else}Send error
          {/if}
        </h2>
        <div class="modal__route">slot {smp.slot.toString().padStart(2, '0')} · {connectionLabel}</div>
      </header>
      <div class="modal__body">
        {#if sendPhase === 'sending'}
          <div class="modal__step">{sendStatus || 'Working…'}</div>
          {#if sendKind === 'full'}
            <!-- Synthetic progress bar — ticks against the estimated
                 upload time. PutSampleOpenLoop is open-loop (the OS
                 queues the full envelope in one shot), so no real
                 byte counter is available. Same heuristic as Copy:
                 caps at 99% until the wire call returns, then snaps
                 to 100% via the 'done' phase. -->
            <div class="progress">
              <div class="progress__bar" style="width: {sendPercent}%;"></div>
              <div class="progress__label">
                {Math.floor(sendPercent)}% · {Math.max(0, Math.round(sendElapsedMs / 1000))}s
                / ~{estimatedCopySeconds(smp.length)}s
              </div>
            </div>
          {/if}
          <div class="modal__hint">
            Don't close this window. Upload is open-loop — the OS buffers the
            full envelope, then the device parses it block by block.
          </div>
        {:else if sendPhase === 'done'}
          <div class="modal__step">
            ✓ {sendKind === 'full'
              ? `Audio + SPRM written to slot ${smp.slot}.`
              : `Parameters written to slot ${smp.slot}.`}
          </div>
        {:else}
          <div class="modal__step modal__step--error">{sendError}</div>
        {/if}
      </div>
      <footer class="modal__foot">
        {#if sendPhase === 'sending'}
          <span class="modal__waitnote">do not close · upload in progress</span>
        {:else}
          <button type="button" class="btn btn--primary" on:click={closeSendModal}>Close</button>
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
  /* Below ~1100px the 6-column identity strip squeezes every cell
     to ~80px, which is narrower than the formatted values inside
     (e.g. "26.04 kHz", "C3 (960)"). Stack to a 3-column / 2-row
     grid so values stay on one line. */
  @media (max-width: 1100px) {
    .identity {
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: 10px 14px;
    }
    .identity__cell--preview { grid-column: 3; }
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
  /* Active styling while audio is in flight. Matches the hover
     colour so the visual language stays consistent — yellow =
     idle/ready, magenta = active. Pulses subtly so a long-running
     loop preview reads as "still playing" rather than "stuck on". */
  .identity__play--active {
    background: var(--rb-magenta);
    color: var(--paper);
    animation: identity-play-pulse 1.2s ease-in-out infinite;
  }
  @keyframes identity-play-pulse {
    0%, 100% { box-shadow: 0 0 0 0 rgba(255, 0, 152, 0.6); }
    50%      { box-shadow: 0 0 0 4px rgba(255, 0, 152, 0); }
  }
  .identity__play[disabled] {
    background: var(--grey-light);
    color: var(--grey-medium);
    cursor: not-allowed;
  }
  /* Play button + mode chip on one row. The chip is the standard
     chip--select pattern (transparent <select> over a styled label),
     just sized down for the identity cell's narrow column. */
  .preview-row {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  :global(.preview-source) {
    font-size: 11px;
    padding: 1px 18px 1px 8px;
    height: 26px;
  }
  /* Rate dropdown in the identity strip. Sized to match the static
     .field display it replaces so the strip's column widths don't
     reflow when a sample flips between local (dropdown) and device
     (static text). */
  :global(.identity__rate-select) {
    font-size: 12px;
    padding: 2px 18px 2px 8px;
    min-height: 22px;
  }
  /* Device-mode mapping affordance. Sits below the play row, small
     enough to feel like supplementary info rather than a primary
     control. Two states: --ok (mapping discovered, green-ish text)
     and --warn (no mapping, muted red — the device will be silent). */
  .preview-hint {
    margin-top: 4px;
    font-size: 10px;
    line-height: 1.3;
    color: var(--grey-medium);
  }
  .preview-hint--ok strong { color: var(--ink); }
  .preview-hint--warn { color: var(--rb-magenta); }
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
    /* --scroll-h is the height the horizontal scrollbar steals from
       the bottom of the waveform when zoom > 1. Every overlay that
       anchors to the ruler at bottom: 18px adds this so the bar
       doesn't visually mask their bottom 10px. */
    --scroll-h: 0px;
  }
  .waveform.is-zoomed { --scroll-h: 10px; }
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
  /* When the host has audio that came from the device (Apply Slicing
     write-back OR Copy from S950 download), promote the tag to a
     green-accented "synced" badge so the user can see at a glance
     that the displayed waveform is real AND the S950 has identical
     bytes — distinct from a local-only import. */
  .waveform__preview-tag--synced {
    border-color: var(--rb-green);
    color: var(--ink);
    font-style: normal;
    font-weight: 500;
  }
  .sync-dot {
    width: 6px; height: 6px;
    border-radius: 50%;
    background: var(--rb-green);
    border: 1px solid var(--ink);
    flex-shrink: 0;
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
    top: 0; bottom: calc(18px + var(--scroll-h, 0px));
    left: var(--loop-start);
    width: var(--loop-width);
    background: color-mix(in srgb, var(--rb-cyan) 14%, transparent);
    border-left: 1px solid var(--rb-cyan);
    border-right: 1px solid var(--rb-cyan);
  }
  .waveform__inactive {
    position: absolute;
    top: 0; bottom: calc(18px + var(--scroll-h, 0px));
    background: rgba(0,0,0,0.55);
  }
  .waveform__inactive--pre  { left: 0; width: var(--start); }
  .waveform__inactive--post { right: 0; width: calc(100% - var(--end)); }

  .marker {
    position: absolute;
    top: 0; bottom: calc(18px + var(--scroll-h, 0px));
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

  /* Horizontal scrollbar for the zoomed-in waveform. Sits just above
     the ruler. The track covers the full sample's normalized [0..1]
     range; the yellow thumb shows the visible window and is dragged
     to pan. Pattern lifted from PB950's Waveform component. */
  .waveform__scroll {
    position: absolute;
    left: 0; right: 0;
    bottom: 18px;
    height: 10px;
    z-index: 6;
    background: rgba(0, 0, 0, 0.35);
    cursor: pointer;
  }
  .waveform__scroll-thumb {
    position: absolute;
    top: 1px; bottom: 1px;
    min-width: 16px;
    background: color-mix(in srgb, var(--rb-yellow) 45%, transparent);
    border-radius: 3px;
    cursor: grab;
  }
  .waveform__scroll-thumb:hover  { background: color-mix(in srgb, var(--rb-yellow) 60%, transparent); }
  .waveform__scroll-thumb:active { background: color-mix(in srgb, var(--rb-yellow) 80%, transparent); cursor: grabbing; }
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
    /* Bumped (360 → 420) so the Device & File card's full button
       stack (Apply + Import + Copy + Save + Send + Get SPRM, plus
       the divider) fits without truncating the last button when
       slicing is active. The Slices/Playback cards are still comfortably
       bounded — they typically settle below this cap. */
    max-height: 420px;
    overflow-y: auto;
    /* Force visible thin scrollbars instead of macOS overlay style
       so when content DOES overflow, the user sees the affordance
       and knows to scroll. Same pattern as the Keygroup panels. */
    scrollbar-width: thin;
    scrollbar-color: var(--grey-medium) transparent;
  }
  .controls :global(.card)::-webkit-scrollbar { width: 8px; }
  .controls :global(.card)::-webkit-scrollbar-track { background: transparent; }
  .controls :global(.card)::-webkit-scrollbar-thumb {
    background: var(--grey-medium);
    border-radius: 4px;
    border: 2px solid var(--white);
  }
  .controls :global(.card)::-webkit-scrollbar-thumb:hover { background: var(--grey-dark); }

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
  /* Selected-slice Start/Length rows live inside the slice-pair col,
     which is half a 1fr column inside the 3-up controls grid. The
     default .row label column (120px) leaves only ~70px for the
     6-digit-comma NumField + stepper at 1280px. Narrow the label
     to 88px to give the value room. Scoped to slice-pair so the
     Playback card's Tune/Loudness rows are unaffected. */
  .slice-pair__col :global(.row) {
    grid-template-columns: 88px 1fr;
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
    top: 0; bottom: calc(18px + var(--scroll-h, 0px));
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
    top: 18px; bottom: calc(18px + var(--scroll-h, 0px));
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

  /* Playback playhead — vertical line driven by the preview rAF
     tracker; rendered solid (vs the dashed hover cursor) and in the
     primary accent colour so it reads as "live audio" at a glance.
     z-index sits above the inactive overlays + markers but below
     any modal so a Copy-from-S950 prompt still covers it. */
  .waveform__playhead {
    position: absolute;
    top: 0;
    bottom: calc(18px + var(--scroll-h, 0px));
    width: 2px;
    pointer-events: none;
    z-index: 2;
    background: var(--rb-yellow);
    box-shadow: 0 0 6px rgba(255, 222, 0, 0.55);
  }

  /* Hover playhead — tracks the mouse over the waveform when slicing
     is active. Wheel-zoom anchors at this position; the badge shows
     the word at the cursor (and zoom level if not 100%). */
  .waveform__cursor {
    position: absolute;
    top: 0;
    bottom: calc(18px + var(--scroll-h, 0px));
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
    /* Loop column pinned to 72px ('ping-pong' worst case) so the
       range cell gets a stable min-width: 0 1fr. The range span
       below ellipsises long values instead of forcing the row past
       its container (was overflowing into a horizontal scrollbar
       at narrow .slice-pair__col widths). */
    grid-template-columns: 28px minmax(0, 1fr) 72px 22px 22px;
    align-items: center;
    gap: 6px;
    padding: 3px 8px;
    cursor: pointer;
  }
  .slice-list__row > .slice-list__range {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
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
  /* Segmented toggle row — Once / Loop / Ping-pong (and the Mode
     equivalent on the Playback card). Lives in the value cell of a
     standard .row grid so the buttons align horizontally with the
     NumField value boxes above. flex: 1 1 0 on the toggles makes
     them share the cell width equally, so they always sit on one
     line and resize together as the panel narrows; min-width: 0 +
     ellipsis lets the longest label (PING-PONG) truncate gracefully
     at very tight widths instead of overflowing the row. */
  .mode-row {
    display: flex;
    width: 100%;
    gap: 4px;
    flex-wrap: nowrap;
    min-width: 0;
  }
  .mode-row :global(.toggle) {
    flex: 1 1 0;
    min-width: 0;
    padding: 3px 6px;
    text-align: center;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
