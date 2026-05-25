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
  ApplySlicing:     vi.fn().mockResolvedValue(undefined),
  Catalog:          vi.fn().mockResolvedValue({ programs: [], samples: [] }),
  Connect:          vi.fn().mockResolvedValue(undefined),
  Disconnect:       vi.fn().mockResolvedValue(undefined),
  GetProgram:       vi.fn().mockResolvedValue({}),
  GetSampleParams:  vi.fn().mockResolvedValue({}),
  ImportSample:     vi.fn().mockResolvedValue(null), // user-cancel by default
  InspectSlicing:   vi.fn().mockResolvedValue({ ok: true }),
  ListPorts:        vi.fn().mockResolvedValue({ ins: [], outs: [] }),
  SetProgram:       vi.fn().mockResolvedValue(undefined),
  SetSampleParams:  vi.fn().mockResolvedValue(undefined),
  Status:           vi.fn().mockResolvedValue({ connected: false, channel: 0 }),
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

beforeEach(() => {
  vi.clearAllMocks();
  samples.set([]);
  selectedSampleSlot.set(0);
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
    const empty = container.querySelector('.sample-empty');
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
    const hint = container.querySelector('.dropzone-hint') as HTMLElement;
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
    const hint = container.querySelector('.dropzone-hint') as HTMLElement;
    // dataTransfer with no 'Files' in types — text selection drag.
    await fireEvent.dragEnter(hint, { dataTransfer: { types: ['text/plain'] } });
    await tick();
    expect(hint.classList.contains('is-dragging')).toBe(false);
    cleanup();
  });

  it('counter survives nested-element dragenter/leave traversal', async () => {
    withSourceSample();
    const { container } = render(Sample);
    const hint = container.querySelector('.dropzone-hint') as HTMLElement;

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

// ---------- Helpers ----------

// jsdom's DragEvent constructor accepts dataTransfer but doesn't
// populate the `types` array the way browsers do. We build a minimal
// DataTransfer-shaped object that the drag handler's
// `e.dataTransfer?.types.includes('Files')` check actually reads.
function makeFileDT(): DataTransfer {
  return { types: ['Files'] } as unknown as DataTransfer;
}
