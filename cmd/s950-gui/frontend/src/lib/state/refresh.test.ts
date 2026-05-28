// Tests for the full-device refresh state machine. Covers phase
// progression, cancellation, error capture, and the audio
// skip-when-cached short-circuit. Wails App methods are stubbed —
// the real wire round-trips are hardware-validated, not here.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';

// Mocks must precede the SUT import so the module under test picks
// them up. Each App method is a vi.fn() so individual tests can
// re-mock per scenario.
vi.mock('../../../wailsjs/go/main/App', () => ({
  Catalog:           vi.fn(),
  GetProgram:        vi.fn(),
  GetSampleParams:   vi.fn(),
  CopySampleAudio:   vi.fn(),
  PutCachedWaveform: vi.fn().mockResolvedValue(undefined),
}));

// refresh.ts pulls in scanMemory, which gates itself on the
// connection phase being 'connected'. Stub it to a no-op so the
// tests don't have to spin up the whole connection store.
vi.mock('./memory', () => ({
  scanMemory: vi.fn().mockResolvedValue(undefined),
}));

// catalog.ts has cascading dependencies (programs, samples,
// memory, livesync). Stub just the two helpers refresh.ts uses.
vi.mock('./catalog', () => ({
  refreshCatalog:      vi.fn().mockResolvedValue(undefined),
  ensureProgramLoaded: vi.fn().mockResolvedValue(undefined),
}));

import * as App from '../../../wailsjs/go/main/App';
import * as catalog from './catalog';
import {
  refreshState,
  refreshAllFromDevice,
  cancelRefresh,
  resetRefresh,
} from './refresh';
import { programs } from './programs';
import { samples, newLocalSample, type Sample } from './samples';

beforeEach(() => {
  vi.clearAllMocks();
  programs.set([]);
  samples.set([]);
  resetRefresh();
});

// Helper: install a device-sourced sample without host audio so
// the audio-pass actually has work to do. `pcm: undefined` is the
// signal refresh.ts uses to decide "needs audio."
function deviceSample(slot: number, name = 'KICK', length = 1000): Sample {
  return {
    ...newLocalSample(slot, name, 26040, length),
    source: 'device',
  };
}

describe('refreshAllFromDevice — phase progression', () => {
  it('starts idle, goes through catalog → samples → programs → audio → done', async () => {
    programs.set([{ slot: 0, name: 'P0', midiProg: 1, respondPC: false, keyTilt: 0, positionalXfade: false, keygroups: [] }]);
    samples.set([deviceSample(5)]);

    ((App as any).CopySampleAudio as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce([0x800, 0x800, 0x800]);

    await refreshAllFromDevice();

    const final = get(refreshState);
    expect(final.phase).toBe('done');
    expect(final.progress).toBe(100);

    // Each step ran exactly once.
    expect(catalog.refreshCatalog).toHaveBeenCalledTimes(1);
    expect(catalog.ensureProgramLoaded).toHaveBeenCalledTimes(1);
    expect(catalog.ensureProgramLoaded).toHaveBeenCalledWith(0, true);
    expect((App as any).CopySampleAudio).toHaveBeenCalledTimes(1);
    expect((App as any).CopySampleAudio).toHaveBeenCalledWith(5);
  });

  it('counts programs in subTotal/subCurrent while iterating', async () => {
    programs.set([
      { slot: 0, name: 'P0', midiProg: 1, respondPC: false, keyTilt: 0, positionalXfade: false, keygroups: [] },
      { slot: 1, name: 'P1', midiProg: 1, respondPC: false, keyTilt: 0, positionalXfade: false, keygroups: [] },
      { slot: 2, name: 'P2', midiProg: 1, respondPC: false, keyTilt: 0, positionalXfade: false, keygroups: [] },
    ]);

    // Capture the message at the moment ensureProgramLoaded is
    // called so we can verify subCurrent advances 1→2→3.
    const messagesAtCall: string[] = [];
    (catalog.ensureProgramLoaded as ReturnType<typeof vi.fn>).mockImplementation(async () => {
      messagesAtCall.push(get(refreshState).message);
    });

    await refreshAllFromDevice();
    expect(messagesAtCall.length).toBe(3);
    expect(messagesAtCall[0]).toContain('1/3');
    expect(messagesAtCall[1]).toContain('2/3');
    expect(messagesAtCall[2]).toContain('3/3');
  });
});

describe('refreshAllFromDevice — audio skip-when-cached', () => {
  it('skips samples that already have host PCM', async () => {
    const withAudio = {
      ...deviceSample(3, 'CACHED'),
      pcm: [0, 1, 2],
      words12: [0x800, 0x800, 0x800],
    };
    const withoutAudio = deviceSample(4, 'FRESH');
    samples.set([withAudio, withoutAudio]);

    ((App as any).CopySampleAudio as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce([0x800, 0x800, 0x800]);

    await refreshAllFromDevice();
    // Only slot 4 should have been fetched — slot 3 already has PCM.
    expect((App as any).CopySampleAudio).toHaveBeenCalledTimes(1);
    expect((App as any).CopySampleAudio).toHaveBeenCalledWith(4);
  });

  it('skips local-only samples entirely (never on the device)', async () => {
    const local = { ...newLocalSample(0, 'IMPORT', 26040, 1000), source: 'local' as const };
    samples.set([local]);

    await refreshAllFromDevice();
    expect((App as any).CopySampleAudio).not.toHaveBeenCalled();
  });

  it('skips the entire audio phase when called with withAudio=false', async () => {
    // Modal's "Include sample audio" toggle off → metadata-only.
    // The progress-bar machinery should hop straight to done after
    // programs without entering the audio phase at all. Include
    // both a program and a sample so the message-asserting
    // "audio skipped" branch fires (the empty-device path has a
    // different message).
    programs.set([
      { slot: 0, name: 'P', midiProg: 1, respondPC: false, keyTilt: 0, positionalXfade: false, keygroups: [] },
    ]);
    samples.set([deviceSample(5, 'WOULD-PULL')]);
    await refreshAllFromDevice(false);
    expect((App as any).CopySampleAudio).not.toHaveBeenCalled();
    expect(get(refreshState).phase).toBe('done');
    expect(get(refreshState).message).toMatch(/audio skipped/i);
  });

  it('attaches words+pcm + caches each pulled sample', async () => {
    samples.set([deviceSample(5, 'TONE', 3)]);

    // 0x800 = silence (encodes to 0 PCM); 0xC00/0x400 are positive
    // and negative excursions to verify the int16 scaling.
    ((App as any).CopySampleAudio as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce([0x800, 0xC00, 0x400]);

    await refreshAllFromDevice();

    const s = get(samples).find((x) => x.slot === 5);
    expect(s?.words12).toEqual([0x800, 0xC00, 0x400]);
    expect(s?.pcm?.length).toBe(3);
    expect((App as any).PutCachedWaveform).toHaveBeenCalledWith(5, 'TONE', 3, [0x800, 0xC00, 0x400]);
  });
});

describe('refreshAllFromDevice — cancel + error', () => {
  it('cancel between phases stops the run and reports cancelled', async () => {
    programs.set([
      { slot: 0, name: 'P0', midiProg: 1, respondPC: false, keyTilt: 0, positionalXfade: false, keygroups: [] },
    ]);
    samples.set([]);

    // Cancel mid-loop: fire after the first ensureProgramLoaded
    // call so the loop boundary check sees the flag and exits.
    (catalog.ensureProgramLoaded as ReturnType<typeof vi.fn>).mockImplementation(async () => {
      cancelRefresh();
    });

    await refreshAllFromDevice();
    expect(get(refreshState).phase).toBe('idle');
    expect(get(refreshState).message).toMatch(/cancel/i);
    // Audio phase never started — CopySampleAudio not called even
    // though there'd otherwise be a sample to pull.
    expect((App as any).CopySampleAudio).not.toHaveBeenCalled();
  });

  it('catches and surfaces errors from refreshCatalog', async () => {
    (catalog.refreshCatalog as ReturnType<typeof vi.fn>).mockRejectedValueOnce(
      new Error('catalog unreachable'),
    );

    await refreshAllFromDevice();
    const final = get(refreshState);
    expect(final.phase).toBe('error');
    expect(final.error).toContain('catalog unreachable');
  });

  it('continues past a failing program (logged, not fatal)', async () => {
    programs.set([
      { slot: 0, name: 'OK',   midiProg: 1, respondPC: false, keyTilt: 0, positionalXfade: false, keygroups: [] },
      { slot: 1, name: 'BAD',  midiProg: 1, respondPC: false, keyTilt: 0, positionalXfade: false, keygroups: [] },
      { slot: 2, name: 'OK2',  midiProg: 1, respondPC: false, keyTilt: 0, positionalXfade: false, keygroups: [] },
    ]);

    (catalog.ensureProgramLoaded as ReturnType<typeof vi.fn>).mockImplementation(async (slot: number) => {
      if (slot === 1) throw new Error('NAK');
    });

    await refreshAllFromDevice();
    // All three programs were attempted — one failure didn't abort
    // the rest of the loop.
    expect(catalog.ensureProgramLoaded).toHaveBeenCalledTimes(3);
    expect(get(refreshState).phase).toBe('done');
  });

  it('continues past a failing audio copy (logged, not fatal)', async () => {
    samples.set([deviceSample(0, 'A'), deviceSample(1, 'B'), deviceSample(2, 'C')]);
    ((App as any).CopySampleAudio as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce([0x800])             // slot 0 OK
      .mockRejectedValueOnce(new Error('timeout')) // slot 1 fails
      .mockResolvedValueOnce([0x800]);            // slot 2 OK

    await refreshAllFromDevice();
    expect((App as any).CopySampleAudio).toHaveBeenCalledTimes(3);
    expect(get(refreshState).phase).toBe('done');
  });
});
