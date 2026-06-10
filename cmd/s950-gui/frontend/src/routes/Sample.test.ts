// Component tests for the Sample tab. Focused on the recent UX
// additions where the wiring is non-obvious from reading the code:
//   • empty-state CTA when no samples are loaded
//   • LOCAL vs on-S950 sync indicator on sidebar rows
//   • drag-over hover state on the dropzone hint + waveform card
//   • Slice mode toggle
//   • Commit Slices button — visibility + behaviour, lockout after commit
//
// What's NOT covered: pixel-precise drag-coordinate interactions
// (slice marker drag, loop edge drag, marker drag). Those depend on
// getBoundingClientRect math that jsdom doesn't model accurately —
// the unit-level behaviour is exercised through slicing.test.ts
// and exercised end-to-end in the dev app.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { tick } from 'svelte';
import { render, fireEvent, cleanup } from '@testing-library/svelte';
import { get } from 'svelte/store';

// Hoisted mocks must precede component imports. Tests import the
// stores/utilities they assert against AFTER these mocks land.
vi.mock('../../wailsjs/go/main/App', () => ({
  ApplySlicing:        vi.fn().mockResolvedValue(undefined),
  Catalog:             vi.fn().mockResolvedValue({ programs: [], samples: [] }),
  Connect:             vi.fn().mockResolvedValue(undefined),
  CopySampleAudio:     vi.fn().mockResolvedValue([]),
  Disconnect:          vi.fn().mockResolvedValue(undefined),
  GetCachedWaveform:   vi.fn().mockResolvedValue(null),
  GetProgram:          vi.fn().mockResolvedValue({}),
  GetSampleParams:     vi.fn().mockResolvedValue({}),
  ImportSample:        vi.fn().mockResolvedValue(null), // user-cancel by default
  InspectSlicing:      vi.fn().mockResolvedValue({ ok: true }),
  ListPorts:           vi.fn().mockResolvedValue({ ins: [], outs: [] }),
  PutCachedWaveform:   vi.fn().mockResolvedValue(undefined),
  SendSample:          vi.fn().mockResolvedValue(undefined),
  SetProgram:          vi.fn().mockResolvedValue(undefined),
  SetSampleParams:     vi.fn().mockResolvedValue(undefined),
  Status:              vi.fn().mockResolvedValue({ connected: false, channel: 0 }),
}));

vi.mock('../../wailsjs/runtime/runtime', () => ({
  EventsOn:       vi.fn(() => () => {}),
  EventsOff:      vi.fn(),
  OnFileDrop:     vi.fn(),
  OnFileDropOff:  vi.fn(),
}));

import * as App from '../../wailsjs/go/main/App';
import Sample from './Sample.svelte';
import {
  samples,
  selectedSampleSlot,
  newLocalSample,
  type Sample as SampleT,
} from '../lib/state/samples';
import { slicing, updateSlicing, buildEvenSlices } from '../lib/state/slicing';
import { transportKind } from '../lib/state/connection';

beforeEach(() => {
  vi.clearAllMocks();
  samples.set([]);
  selectedSampleSlot.set(0);
  // Most tests assume the topbar is on the MIDI path (the
  // default). Tests that exercise Copy-from-S950 flip this to
  // 'serial' explicitly — the button is hidden on MIDI by design.
  transportKind.set('midi');
  // Reset any slicing state left by a previous test.
  updateSlicing({ active: false, slices: [], selectedIndex: 0 });
});

// Helper: install a single source sample with synthetic audio.
function withSourceSample(name = 'KICK', length = 1000): SampleT {
  const pcm = Array.from({ length }, (_, i) => i);
  const words12 = Array.from({ length }, (_, i) => (i & 0xfff));
  const s: SampleT = {
    ...newLocalSample(0, name, 26040, length),
    source: 'local',
    pcm,
    words12,
  };
  samples.set([s]);
  selectedSampleSlot.set(0);
  return s;
}

describe('Sample tab — empty state', () => {
  it('renders the CTA when no samples are loaded', () => {
    const { getByText, container } = render(Sample);
    expect(getByText(/No samples loaded/i)).toBeTruthy();
    // The drop-target affordance carries the Wails CSS property.
    const empty = container.querySelector('.empty-state');
    expect(empty?.getAttribute('style')).toContain('--wails-drop-target');
    cleanup();
  });

  it('Import button on the empty state calls App.ImportSample("")', async () => {
    const { getByRole } = render(Sample);
    const btn = getByRole('button', { name: /Import \.wav/i });
    await fireEvent.click(btn);
    expect(App.ImportSample).toHaveBeenCalledWith('');
    cleanup();
  });
});

describe('Sample tab — sidebar sync indicators', () => {
  it('renders LOCAL tag for local samples and rate for device samples', () => {
    samples.set([
      { ...newLocalSample(0, 'A', 26040, 1000), source: 'local' },
      { ...newLocalSample(1, 'B', 26040, 1000), source: 'device' },
    ]);
    selectedSampleSlot.set(0);
    const { container } = render(Sample);
    const rows = Array.from(container.querySelectorAll('.sample-list .sample'));
    expect(rows.length).toBeGreaterThanOrEqual(2);
    const localRow  = rows.find((r) => r.textContent?.includes('A'));
    const deviceRow = rows.find((r) => r.textContent?.includes('B'));
    expect(localRow?.querySelector('.sample__tag--local')).toBeTruthy();
    expect(deviceRow?.querySelector('.sample__tag--local')).toBeFalsy();
    expect(deviceRow?.querySelector('.sample__rate')).toBeTruthy();
    cleanup();
  });

  it('identity strip echoes the source state in the Slot field', () => {
    withSourceSample();
    const { container } = render(Sample);
    const slotCell = container.querySelector('.field--source-local');
    expect(slotCell?.textContent).toMatch(/local only/i);
    cleanup();
  });
});

describe('Sample tab — drag-over hover state', () => {
  it('adds is-dragging to the dropzone hint on dragenter with Files', async () => {
    withSourceSample();
    const { container } = render(Sample);
    const hint = container.querySelector('.sidebar__panel') as HTMLElement;
    expect(hint).toBeTruthy();
    expect(hint.classList.contains('is-dragging')).toBe(false);

    // jsdom has no native DragEvent constructor; testing-library's
    // fireEvent.dragEnter/Leave build a properly-typed event from
    // the provided properties.
    await fireEvent.dragEnter(hint, { dataTransfer: makeFileDT() });
    await tick();
    expect(hint.classList.contains('is-dragging')).toBe(true);

    await fireEvent.dragLeave(hint, { dataTransfer: makeFileDT() });
    await tick();
    expect(hint.classList.contains('is-dragging')).toBe(false);
    cleanup();
  });

  it('ignores drags that do not carry Files (e.g. text selections)', async () => {
    withSourceSample();
    const { container } = render(Sample);
    const hint = container.querySelector('.sidebar__panel') as HTMLElement;
    // dataTransfer with no 'Files' in types — text selection drag.
    await fireEvent.dragEnter(hint, { dataTransfer: { types: ['text/plain'] } });
    await tick();
    expect(hint.classList.contains('is-dragging')).toBe(false);
    cleanup();
  });

  it('counter survives nested-element dragenter/leave traversal', async () => {
    withSourceSample();
    const { container } = render(Sample);
    const hint = container.querySelector('.sidebar__panel') as HTMLElement;

    // Two enters (root + child traversal), one leave: counter > 0 → still hot.
    await fireEvent.dragEnter(hint, { dataTransfer: makeFileDT() });
    await fireEvent.dragEnter(hint, { dataTransfer: makeFileDT() });
    await fireEvent.dragLeave(hint, { dataTransfer: makeFileDT() });
    await tick();
    expect(hint.classList.contains('is-dragging')).toBe(true);
    cleanup();
  });
});

describe('Sample tab — slice toggle', () => {
  it('clicking ●/○ Slice flips slicing.active in the store', async () => {
    withSourceSample('KICK', 8000);
    const { getByRole } = render(Sample);
    const btn = getByRole('button', { name: /Slice$/i });
    expect(get(slicing).active).toBe(false);
    await fireEvent.click(btn);
    await tick();
    expect(get(slicing).active).toBe(true);
    cleanup();
  });
});

describe('Sample tab — commit + apply lifecycle', () => {
  function setupActiveSlicing(numSlices = 4) {
    withSourceSample('KICK', 1000);
    updateSlicing({
      active: true,
      slices: buildEvenSlices(numSlices, 1000),
      selectedIndex: 0,
    });
  }

  it('shows Commit when slicing is active + slices exist', async () => {
    setupActiveSlicing(4);
    const { getByRole } = render(Sample);
    const commit = getByRole('button', { name: /Commit 4 slices/i });
    expect(commit).toBeTruthy();
    cleanup();
  });

  it('Apply label adapts based on commit state', async () => {
    setupActiveSlicing(4);
    const { getByRole, queryByRole } = render(Sample);
    // Pre-commit: text reads "Apply slicing →".
    expect(queryByRole('button', { name: /Apply slicing →/i })).toBeTruthy();

    // Click Commit. The lifecycle: children appear, slicing deactivates,
    // selection stays on source, button label changes to "Apply → upload".
    const commit = getByRole('button', { name: /Commit 4 slices/i });
    await fireEvent.click(commit);
    await tick();

    const children = get(samples).filter((s) => s.parentSlot === 0);
    expect(children).toHaveLength(4);
    expect(get(slicing).active).toBe(false);
    // Commit button is gone (slicing inactive); Apply remains, relabeled.
    expect(queryByRole('button', { name: /Commit/i })).toBeNull();
    expect(queryByRole('button', { name: /Apply → upload 4 samples/i })).toBeTruthy();
    cleanup();
  });

  it('Apply success removes committed children and calls App.ApplySlicing', async () => {
    setupActiveSlicing(2);
    const { getByRole } = render(Sample);

    // Commit first.
    await fireEvent.click(getByRole('button', { name: /Commit 2 slices/i }));
    await tick();
    expect(get(samples).filter((s) => s.parentSlot === 0)).toHaveLength(2);

    // Apply → opens preflight modal. Resolve mocks both calls.
    await fireEvent.click(getByRole('button', { name: /Apply → upload 2 samples/i }));
    await tick();
    expect(App.InspectSlicing).toHaveBeenCalled();

    // Click Continue on the preflight modal to actually run Apply.
    const cont = getByRole('button', { name: /Continue/i });
    await fireEvent.click(cont);
    // Microtask: ApplySlicing resolves, then cleanup runs.
    await tick();
    await tick();
    expect(App.ApplySlicing).toHaveBeenCalled();
    // Children removed — sidebar only carries the source again.
    expect(get(samples).filter((s) => s.parentSlot === 0)).toHaveLength(0);
    cleanup();
  });
});

describe('Sample tab — Copy from S950 (Phase 1C)', () => {
  // Helper: install a device-sourced sample at slot 5 with no host
  // audio. Matches the post-refreshCatalog shape for samples we
  // didn't upload ourselves (factory programs, front-panel-recorded
  // sounds). The Copy button is the only way to get real audio for
  // these rows without re-uploading.
  function withDeviceSample(slot = 5, name = 'TONE', length = 5000): SampleT {
    const s: SampleT = {
      ...newLocalSample(slot, name, 26040, length),
      source: 'device',
    };
    samples.set([s]);
    selectedSampleSlot.set(slot);
    // Copy-from-S950 is only viable over RS-232 (MIDI's ACK pump
    // corrupts large samples mid-stream), so the button is hidden
    // unless we're on serial. Tests in this suite are about the
    // Copy flow itself — they need the button visible.
    transportKind.set('serial');
    return s;
  }

  it('hides the Copy button when the transport is MIDI', () => {
    samples.set([
      { ...newLocalSample(5, 'TONE', 26040, 5000), source: 'device' },
    ]);
    selectedSampleSlot.set(5);
    transportKind.set('midi');
    const { container } = render(Sample);
    const btn = Array.from(container.querySelectorAll('button.btn'))
      .find((b) => /copy from s950/i.test(b.textContent ?? ''));
    expect(btn).toBeFalsy();
    cleanup();
  });

  it('renders the button as "Copy from S950" (not "Replace")', () => {
    withDeviceSample();
    const { container } = render(Sample);
    const btns = Array.from(container.querySelectorAll('button.btn'));
    const copyBtn = btns.find((b) => /copy from s950/i.test(b.textContent ?? ''));
    expect(copyBtn).toBeTruthy();
    // Defensive: the old "Replace from S950" wording is gone.
    expect(btns.find((b) => /replace from s950/i.test(b.textContent ?? ''))).toBeFalsy();
    cleanup();
  });

  it('disables the button on LOCAL ONLY samples', () => {
    // A local sample has no device-side counterpart — copying would
    // be undefined behaviour at best, accidentally clobber the
    // user's local work at worst. We're on serial here so the
    // button is visible (it's hidden on MIDI for a separate reason).
    samples.set([
      { ...newLocalSample(0, 'IMPORTED', 26040, 1000), source: 'local' },
    ]);
    selectedSampleSlot.set(0);
    transportKind.set('serial');
    const { container } = render(Sample);
    const btn = Array.from(container.querySelectorAll('button.btn'))
      .find((b) => /copy from s950/i.test(b.textContent ?? '')) as HTMLButtonElement;
    expect(btn?.disabled).toBe(true);
    expect(btn?.getAttribute('title') ?? '').toMatch(/select an on-device sample/i);
    cleanup();
  });

  it('enables the button on device samples', () => {
    withDeviceSample();
    const { container } = render(Sample);
    const btn = Array.from(container.querySelectorAll('button.btn'))
      .find((b) => /copy from s950/i.test(b.textContent ?? '')) as HTMLButtonElement;
    expect(btn?.disabled).toBe(false);
    cleanup();
  });

  it('click calls CopySampleAudio, attaches words+pcm, persists to cache', async () => {
    withDeviceSample(5, 'TONE', 3);
    // Return a tiny 3-word buffer so the test asserts the exact
    // shape post-attach. 0x800 = silence, 0xC00/0x400 are positive/
    // negative excursions — both should land in pcm with non-zero
    // values (int16-scaled, matching ImportSample's convention).
    ((App as any).CopySampleAudio as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce({ words: [0x800, 0xC00, 0x400], sampleRateHz: 40000 });

    const { container } = render(Sample);
    const btn = Array.from(container.querySelectorAll('button.btn'))
      .find((b) => /copy from s950/i.test(b.textContent ?? '')) as HTMLButtonElement;
    await fireEvent.click(btn);
    // Wait two ticks: one for the async resolve, one for the
    // samples.update + render.
    await tick();
    await tick();

    expect((App as any).CopySampleAudio).toHaveBeenCalledWith(5);
    const s = get(samples).find((x) => x.slot === 5);
    expect(s?.words12).toEqual([0x800, 0xC00, 0x400]);
    expect(s?.pcm?.length).toBe(3);
    expect(s?.pcm?.[0]).toBe(0);            // silence
    expect(s?.pcm?.[1]).toBeGreaterThan(0); // positive
    expect(s?.pcm?.[2]).toBeLessThan(0);    // negative
    // Rate from the dump header overrides the catalog-time SPRM rate.
    expect(s?.rate).toBe(40000);

    // Persistence path: PutCachedWaveform called with the buffer's
    // length (not the sample's possibly-stale length field) and the
    // dump-header rate so the cache hit on next session re-attaches
    // with the right rate.
    expect((App as any).PutCachedWaveform).toHaveBeenCalledWith(5, 'TONE', 3, [0x800, 0xC00, 0x400], 40000);
    cleanup();
  });

  it('reports an error and stays idle when CopySampleAudio rejects', async () => {
    withDeviceSample(7, 'BUSTED');
    ((App as any).CopySampleAudio as ReturnType<typeof vi.fn>)
      .mockRejectedValueOnce(new Error('timeout waiting for sample dump'));

    const { container, findByText } = render(Sample);
    const btn = Array.from(container.querySelectorAll('button.btn'))
      .find((b) => /copy from s950/i.test(b.textContent ?? '')) as HTMLButtonElement;
    await fireEvent.click(btn);

    // The error modal should be visible with the failure message.
    expect(await findByText(/timeout waiting for sample dump/i)).toBeTruthy();
    // Sample row is untouched — no audio attached.
    const s = get(samples).find((x) => x.slot === 7);
    expect(s?.words12).toBeUndefined();
    expect((App as any).PutCachedWaveform).not.toHaveBeenCalled();
    cleanup();
  });
});

// ---------- Send to S950 ----------

describe('Sample tab — Send to S950', () => {
  // Helper: a local sample with host-side PCM/words ready to upload.
  // newLocalSample doesn't attach audio (that's import's job), so we
  // splice in a small fake buffer just to satisfy the words12 guard.
  function withLocalReady(slot = 0, name = 'KICK'): SampleT {
    const s: SampleT = {
      ...newLocalSample(slot, name, 26040, 200),
      source: 'local',
      pcm: [0, 1, 2, 3],
      words12: [0x800, 0x801, 0x802, 0x803],
    };
    samples.set([s]);
    selectedSampleSlot.set(slot);
    return s;
  }

  it('shows "Send to S950" on local samples, "Send SPRM to S950" on device samples', () => {
    // Local: primary upload action.
    withLocalReady();
    let { container } = render(Sample);
    const localBtns = Array.from(container.querySelectorAll('button.btn')).map((b) => b.textContent ?? '');
    expect(localBtns.some((t) => /^send to s950$/i.test(t.trim()))).toBe(true);
    expect(localBtns.some((t) => /^send sprm to s950$/i.test(t.trim()))).toBe(false);
    cleanup();

    // Device: params-only action.
    samples.set([{ ...newLocalSample(5, 'TONE', 26040, 5000), source: 'device' }]);
    selectedSampleSlot.set(5);
    ({ container } = render(Sample));
    const devBtns = Array.from(container.querySelectorAll('button.btn')).map((b) => b.textContent ?? '');
    expect(devBtns.some((t) => /^send to s950$/i.test(t.trim()))).toBe(false);
    expect(devBtns.some((t) => /^send sprm to s950$/i.test(t.trim()))).toBe(true);
    cleanup();
  });

  it('disables Send to S950 when local sample has no host audio', () => {
    samples.set([{ ...newLocalSample(0, 'EMPTY', 26040, 100), source: 'local' }]);
    selectedSampleSlot.set(0);
    const { container } = render(Sample);
    const btn = Array.from(container.querySelectorAll('button.btn'))
      .find((b) => /^send to s950$/i.test((b.textContent ?? '').trim())) as HTMLButtonElement;
    expect(btn).toBeTruthy();
    expect(btn.disabled).toBe(true);
    cleanup();
  });

  it('click calls App.SendSample with slot + words + rate + params', async () => {
    withLocalReady(3, 'SNARE');
    const { container } = render(Sample);
    const btn = Array.from(container.querySelectorAll('button.btn'))
      .find((b) => /^send to s950$/i.test((b.textContent ?? '').trim())) as HTMLButtonElement;
    await fireEvent.click(btn);
    await tick();

    expect((App as any).SendSample).toHaveBeenCalledTimes(1);
    const [slot, words, rate, params] = (App as any).SendSample.mock.calls[0];
    expect(slot).toBe(3);
    expect(words).toEqual([0x800, 0x801, 0x802, 0x803]);
    expect(rate).toBe(26040);
    // SampleParams shape: high-level fields encoded into the
    // wire-style struct. Name space-padded to 10 chars; ReplayMode
    // is the byte for the current mode.
    expect(params.Name).toBe('SNARE     ');
    expect(params.TotalWords).toBe(200);
    expect(params.ReplayMode).toBe(79); // 'O' = one-shot default
    cleanup();
  });

  it('promotes the sample from local to device on successful upload', async () => {
    withLocalReady(7, 'HAT');
    const { container } = render(Sample);
    const btn = Array.from(container.querySelectorAll('button.btn'))
      .find((b) => /^send to s950$/i.test((b.textContent ?? '').trim())) as HTMLButtonElement;
    await fireEvent.click(btn);
    await tick();
    await tick();
    const s = get(samples).find((x) => x.slot === 7);
    expect(s?.source).toBe('device');
    cleanup();
  });

  it('shows an error modal when SendSample rejects', async () => {
    ((App as any).SendSample as ReturnType<typeof vi.fn>)
      .mockRejectedValueOnce(new Error('3 NAKs during upload'));
    withLocalReady(0, 'BUSTED');
    const { container, findByText } = render(Sample);
    const btn = Array.from(container.querySelectorAll('button.btn'))
      .find((b) => /^send to s950$/i.test((b.textContent ?? '').trim())) as HTMLButtonElement;
    await fireEvent.click(btn);
    expect(await findByText(/3 naks during upload/i)).toBeTruthy();
    // Source stays 'local' on failure — the row shouldn't lie about
    // device residency just because the user clicked the button.
    const s = get(samples).find((x) => x.slot === 0);
    expect(s?.source).toBe('local');
    cleanup();
  });

  it('Send SPRM calls SetSampleParams (no audio upload)', async () => {
    samples.set([{ ...newLocalSample(5, 'TONE', 26040, 5000), source: 'device' }]);
    selectedSampleSlot.set(5);
    const { container } = render(Sample);
    const btn = Array.from(container.querySelectorAll('button.btn'))
      .find((b) => /^send sprm to s950$/i.test((b.textContent ?? '').trim())) as HTMLButtonElement;
    await fireEvent.click(btn);
    await tick();

    expect((App as any).SetSampleParams).toHaveBeenCalledTimes(1);
    expect((App as any).SendSample).not.toHaveBeenCalled();
    const [slot, params] = (App as any).SetSampleParams.mock.calls[0];
    expect(slot).toBe(5);
    expect(params.Name).toBe('TONE      ');
    cleanup();
  });
});

// ---------- Helpers ----------

// jsdom's DragEvent constructor accepts dataTransfer but doesn't
// populate the `types` array the way browsers do. We build a minimal
// DataTransfer-shaped object that the drag handler's
// `e.dataTransfer?.types.includes('Files')` check actually reads.
function makeFileDT(): DataTransfer {
  return { types: ['Files'] } as unknown as DataTransfer;
}
