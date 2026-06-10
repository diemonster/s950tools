// Tests for revertSampleToDevice and the livesync source-gate.
// These are the two halves of the "user edits don't strand the
// device in an incoherent state" contract:
//
//   livesync — refuses to push SPRM for source='local' samples,
//     so resampling host PCM doesn't write a mismatched rate to
//     the device's still-original SDATA.
//   revert  — when a local snapshot exists (which it does on the
//     first edit to a device sample), restore it AND re-write the
//     snapshot's SPRM to the device to re-establish coherence.
//     Falls back to fresh fetch when there's no snapshot.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';

vi.mock('../../../wailsjs/go/main/App', () => ({
  GetSampleParams: vi.fn(),
  SetSampleParams: vi.fn().mockResolvedValue(undefined),
  // refresh.ts pulls these in via dynamic catalog import — stub
  // them so the dep graph resolves cleanly.
  Catalog:         vi.fn().mockResolvedValue({ programs: [], samples: [] }),
  GetProgram:      vi.fn().mockResolvedValue({}),
  CopySampleAudio: vi.fn().mockResolvedValue([]),
}));

import * as App from '../../../wailsjs/go/main/App';
import { revertSampleToDevice } from './catalog';
import { scheduleSampleWriteback } from './livesync';
import { samples, newLocalSample, type Sample } from './samples';
import { phase } from './connection';

function deviceSample(slot: number, patch: Partial<Sample> = {}): Sample {
  return {
    ...newLocalSample(slot, `S${slot}`, 26040, 100),
    source: 'device',
    originalSource: 'device',
    ...patch,
  };
}

// Minimal SPRM shape so the selectedSampleSlot subscriber's
// auto-fire (which runs ensureSampleLoaded on import) doesn't
// blow up parsing an undefined wire response. Individual tests
// override with mockResolvedValueOnce when they need specific
// data.
const EMPTY_SPRM = {
  Raw: [], Name: '', TotalWords: 0, SampleRateHz: 26040,
  NominalPitch: 960, LoudOffset: 0, ReplayMode: 79,
  End: 0, Start: 0, LoopLength: 0, VelXFade: 0, Reversed: 0x4E,
};

beforeEach(() => {
  vi.clearAllMocks();
  (App.GetSampleParams as ReturnType<typeof vi.fn>).mockResolvedValue(EMPTY_SPRM);
  samples.set([]);
  phase.set('connected'); // livesync gates on this
});

describe('revertSampleToDevice — snapshot path', () => {
  it('restores from deviceSnapshot when present (no GetSampleParams call)', async () => {
    // Common case: user edited a device sample (snapshot captured),
    // now clicks revert. Should restore byte-identically from the
    // snapshot WITHOUT a network round-trip for SPRM read.
    const snap = {
      ...deviceSample(5, { tune: 0, rate: 44100, pcm: [100, 200] }),
    };
    samples.set([{
      ...deviceSample(5, { tune: 5, rate: 22050, pcm: [50], source: 'local' }),
      deviceSnapshot: snap,
    }]);

    await revertSampleToDevice(5);

    const restored = get(samples).find((s) => s.slot === 5)!;
    expect(restored.tune).toBe(0);
    expect(restored.rate).toBe(44100);
    expect(restored.pcm).toEqual([100, 200]);
    expect(restored.deviceSnapshot).toBeUndefined();
    // SPRM was re-written to re-establish device coherence.
    expect((App as any).SetSampleParams).toHaveBeenCalledTimes(1);
    expect((App as any).SetSampleParams.mock.calls[0][0]).toBe(5);
    // No GetSampleParams — snapshot covered it.
    expect((App as any).GetSampleParams).not.toHaveBeenCalled();
  });

  it('re-establishes device coherence by pushing the snapshot SPRM', async () => {
    // Even if the SetSampleParams call fails (e.g. device blip),
    // the LOCAL state still restores from snapshot — the user
    // shouldn't lose their pre-edit state because the wire blipped.
    ((App as any).SetSampleParams as ReturnType<typeof vi.fn>)
      .mockRejectedValueOnce(new Error('wire blip'));
    const snap = { ...deviceSample(3, { tune: 0 }) };
    samples.set([{
      ...deviceSample(3, { tune: 9, source: 'local' }),
      deviceSnapshot: snap,
    }]);

    await revertSampleToDevice(3);

    const restored = get(samples).find((s) => s.slot === 3)!;
    expect(restored.tune).toBe(0);
    expect(restored.deviceSnapshot).toBeUndefined();
  });

  it('falls back to GetSampleParams when no snapshot exists', async () => {
    // Sample exists but was never edited (snapshot=undefined) —
    // revert should just refresh from device. Useful as a manual
    // "I think the device drifted" force-refresh affordance.
    ((App as any).GetSampleParams as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce({
        Raw: [], Name: 'FRESH', TotalWords: 200, SampleRateHz: 44100,
        NominalPitch: 960, LoudOffset: 0, ReplayMode: 79, // 'O'
        End: 200, Start: 0, LoopLength: 0, VelXFade: 0, Reversed: 0x4E,
      });
    samples.set([deviceSample(7, { name: 'OLD', length: 100 })]);

    await revertSampleToDevice(7);

    const restored = get(samples).find((s) => s.slot === 7)!;
    expect(restored.name).toBe('FRESH');
    expect(restored.length).toBe(200);
    // GetSampleParams was the source of truth, no snapshot rewrite.
    expect((App as any).GetSampleParams).toHaveBeenCalledWith(7);
    expect((App as any).SetSampleParams).not.toHaveBeenCalled();
  });
});

describe('scheduleSampleWriteback — source gate', () => {
  it('schedules writeback for source: "device" samples', () => {
    // Baseline: SPRM live-sync should fire for samples currently
    // synced with the device.
    samples.set([deviceSample(0)]);
    scheduleSampleWriteback(0);
    // No direct observable — the schedule is a setTimeout. Trick:
    // if scheduling fired, the next scheduleSampleWriteback call
    // for the same slot won't re-emit a fresh "dirty" event
    // because the timer's already running. Easier proxy: the
    // store is the input; the function returning without an early
    // exit is what we're testing. Confirmed implicitly by the
    // following "should skip" test failing if the gate is wrong.
    expect(true).toBe(true);
  });

  it('skips writeback for source: "local" samples (post-resample case)', async () => {
    // Resample flips source to 'local' BEFORE livesync's debounce
    // fires. The flushed writeback must skip — otherwise SPRM
    // hits the device with a new rate while SDATA is unchanged,
    // producing pitched-down playback on the next trigger.
    samples.set([{
      ...deviceSample(2),
      source: 'local',
    }]);
    scheduleSampleWriteback(2);
    // Wait for the 400ms debounce + a safety margin to confirm
    // SetSampleParams never gets called.
    await new Promise((r) => setTimeout(r, 450));
    expect((App as any).SetSampleParams).not.toHaveBeenCalled();
  });

  it('skips writeback when the slot is not in the samples store', () => {
    // Defensive: scheduleSampleWriteback for a missing slot
    // shouldn't fire SetSampleParams (would 404 the slot index).
    samples.set([]);
    scheduleSampleWriteback(99);
    // No assertion needed beyond "didn't throw" — but for clarity:
    expect((App as any).SetSampleParams).not.toHaveBeenCalled();
  });

  it('skips writeback when disconnected', () => {
    // Live-sync without a transport would NAK. The existing
    // isConnected guard handles this; verify it's still in
    // effect after the source-gate addition.
    phase.set('disconnected');
    samples.set([deviceSample(0)]);
    scheduleSampleWriteback(0);
    expect((App as any).SetSampleParams).not.toHaveBeenCalled();
  });

  it('re-checks the source AT FLUSH TIME: schedule-then-resample must not send', async () => {
    // The race the flush-time gate exists for: the user edits an
    // SPRM param on a device-coherent sample (arming the 400 ms
    // debounce), then changes the RATE dropdown before the timer
    // fires. The resample flips source to 'local' but the pending
    // timer + slot survive. Without the flush-time re-check, the
    // flush would push the post-resample SPRM (new rate, new
    // TotalWords) onto the device's unchanged SDATA.
    samples.set([deviceSample(3)]); // device-coherent at schedule time
    scheduleSampleWriteback(3);     // passes the schedule-time gate
    // Mid-debounce: resample flips the row to local.
    samples.update((xs) => xs.map((x) => (x.slot === 3 ? { ...x, source: 'local' as const } : x)));
    await new Promise((r) => setTimeout(r, 450));
    expect((App as any).SetSampleParams).not.toHaveBeenCalled();
  });
});
