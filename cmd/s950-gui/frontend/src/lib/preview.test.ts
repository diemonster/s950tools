import { describe, it, expect } from 'vitest';
import { hasHostAudio, buildPingPongLoopBuffer, zohRenderPCM, effectivePreviewOffset } from './preview';
import { newLocalSample } from './state/samples';

// Most of preview.ts is Web Audio-bound and isn't worth mocking
// AudioContext/AudioBufferSource for — those paths are exercised
// manually in the dev app. hasHostAudio is the one pure predicate
// the UI consumes for button-disabled state, so we lock its
// contract here.

describe('hasHostAudio', () => {
  it('returns false for undefined', () => {
    expect(hasHostAudio(undefined)).toBe(false);
  });
  it('returns false when pcm is missing', () => {
    expect(hasHostAudio(newLocalSample(0, 'X', 26040, 100))).toBe(false);
  });
  it('returns false when pcm is empty', () => {
    const s = { ...newLocalSample(0, 'X', 26040, 100), pcm: [] };
    expect(hasHostAudio(s)).toBe(false);
  });
  it('returns true when pcm has content', () => {
    const s = { ...newLocalSample(0, 'X', 26040, 100), pcm: [0, 1, 2] };
    expect(hasHostAudio(s)).toBe(true);
  });
});

// Regression snapshot for the ping-pong loop synthesis. Web Audio has
// no native ping-pong, so previewRegion hand-stitches a buffer; if
// these tests fail, audition of any ping-pong sample/slice will
// regress audibly. Confirmed working in the dev app — the shape here
// is the contract we're locking in.
describe('buildPingPongLoopBuffer', () => {
  it('produces a buffer of length 2 * segLen', () => {
    const src = [10, 20, 30, 40, 50];
    expect(buildPingPongLoopBuffer(src, 0, 5).length).toBe(10);
    expect(buildPingPongLoopBuffer(src, 1, 4).length).toBe(6);
  });

  it('lays out [reverse(endIdx-1..startIdx), forward(startIdx..endIdx-1)]', () => {
    const src = [10, 20, 30, 40, 50];
    const out = Array.from(buildPingPongLoopBuffer(src, 0, 5));
    expect(out).toEqual([50, 40, 30, 20, 10, 10, 20, 30, 40, 50]);
  });

  it('honours a sub-range loop within the source', () => {
    // Source has leading + trailing silence; the loop only covers
    // [100..500] inclusive (indices 2..6 — endIdx=7 exclusive).
    const src = [0, 0, 100, 200, 300, 400, 500, 0, 0];
    const out = Array.from(buildPingPongLoopBuffer(src, 2, 7));
    // Reverse half: 500, 400, 300, 200, 100. Forward half: same shape.
    expect(out).toEqual([500, 400, 300, 200, 100, 100, 200, 300, 400, 500]);
  });

  it('first sample after pre-roll matches the pre-roll\'s last sample (sustained turnaround)', () => {
    // The pre-roll plays src[..endIdx-1] forward, ending at src[endIdx-1].
    // Looping into out[0] should play that same sample once more — the
    // sustained turnaround at the loop apex.
    const src = [10, 20, 30, 40, 50];
    const out = buildPingPongLoopBuffer(src, 0, 5);
    expect(out[0]).toBe(src[5 - 1]); // 50
  });

  it('reverse → forward bounce holds the start sample for one frame', () => {
    // Last reverse sample is src[startIdx] (=10). First forward sample
    // is src[startIdx] (=10) again — the lower-apex sustained turnaround.
    const src = [10, 20, 30, 40, 50];
    const segLen = 5;
    const out = buildPingPongLoopBuffer(src, 0, segLen);
    expect(out[segLen - 1]).toBe(10);
    expect(out[segLen]).toBe(10);
  });

  it('loop wraps cleanly: last forward sample meets first reverse sample', () => {
    // out[last] is the high apex (50), the wrap goes to out[0] which
    // is also 50 — same sustained turnaround as the inside bounce, by
    // construction.
    const src = [10, 20, 30, 40, 50];
    const out = buildPingPongLoopBuffer(src, 0, 5);
    expect(out[out.length - 1]).toBe(out[0]);
  });

  it('clamps a zero-length segment to a 2-sample buffer (degenerate guard)', () => {
    // segLen is Math.max(1, endIdx-startIdx), so even endIdx <= startIdx
    // yields a single-sample reverse + forward pair. Length 2 keeps the
    // AudioBuffer constructor happy if a UI ever passes 0.
    const src = [10, 20, 30];
    const out = buildPingPongLoopBuffer(src, 2, 2);
    expect(out.length).toBe(2);
  });

  it('handles a single-sample loop (segLen=1)', () => {
    const src = [10, 20, 30, 40, 50];
    const out = Array.from(buildPingPongLoopBuffer(src, 2, 3));
    // Reverse half = [src[2]] = [30]; forward half = [src[2]] = [30].
    expect(out).toEqual([30, 30]);
  });

  it('accepts Float32Array input (the real call site)', () => {
    // previewRegion passes AudioBuffer.getChannelData(0) which is a
    // Float32Array, not a plain number[]. Verify the type contract.
    const src = new Float32Array([0.1, 0.2, 0.3, 0.4]);
    const out = buildPingPongLoopBuffer(src, 0, 4);
    expect(out).toBeInstanceOf(Float32Array);
    expect(out.length).toBe(8);
    expect(out[0]).toBeCloseTo(0.4);
    expect(out[7]).toBeCloseTo(0.4);
  });
});

// zohRenderPCM is the host-side emulation of the S950's lack of
// playback anti-aliasing — when a sub-rate sample is rendered into
// a higher-rate AudioBuffer, each source sample is held across the
// rate ratio instead of sinc-interpolated. These tests pin the
// math + edge cases that would otherwise manifest as audio clicks
// (NaN frames) or unexpected pitch shifts.
describe('zohRenderPCM', () => {
  it('passes through samples 1:1 when fromRate === toRate', () => {
    const src = [100, 200, 300, 400];
    const out = new Float32Array(4);
    zohRenderPCM(src, 22050, 22050, out);
    expect(Array.from(out)).toEqual([100 / 32768, 200 / 32768, 300 / 32768, 400 / 32768]);
  });

  it('nearest-neighbor upsamples when fromRate < toRate', () => {
    // 2 source samples → 6 output frames at 3× rate. Each source
    // sample should be held for 3 output frames (zero-order hold).
    const src = [1000, 2000];
    const out = new Float32Array(6);
    zohRenderPCM(src, 10000, 30000, out);
    const scaled = (n: number) => n / 32768;
    expect(Array.from(out)).toEqual([
      scaled(1000), scaled(1000), scaled(1000),
      scaled(2000), scaled(2000), scaled(2000),
    ]);
  });

  it('clamps srcIdx at the last source frame (no NaN on final output)', () => {
    // Without the bounds clamp, the last output frame computed via
    // floor((n-1) * fromRate / toRate) can land at pcm.length and
    // read `undefined`, producing NaN. Audible as a click. Verify
    // the final frame is real, not NaN.
    const src = [500, 1500, 2500];
    const out = new Float32Array(10); // big enough to provoke overrun
    zohRenderPCM(src, 10000, 30000, out);
    for (let i = 0; i < out.length; i++) {
      expect(Number.isFinite(out[i])).toBe(true);
    }
    // The tail should hold the last source value.
    expect(out[out.length - 1]).toBeCloseTo(2500 / 32768);
  });

  it('leaves output untouched for empty source', () => {
    // Defends against a sample with no PCM somehow reaching the
    // renderer. The output buffer is pre-zeroed by createBuffer;
    // we should not write NaN into it. Use 0.5 as the sentinel —
    // float32-exact, so strict equality survives the buffer's
    // implicit quantization.
    const src: number[] = [];
    const out = new Float32Array(8);
    out.fill(0.5);
    zohRenderPCM(src, 22050, 44100, out);
    expect(out.every((v) => v === 0.5)).toBe(true);
  });

  it('downsample path uses pass-through copy capped at out.length', () => {
    // For fromRate > toRate we delegate to the browser's
    // resampler on play; this path just copies what fits. Verifies
    // the truncation doesn't read past either buffer.
    const src = [10, 20, 30, 40, 50, 60];
    const out = new Float32Array(3);
    zohRenderPCM(src, 44100, 22050, out);
    expect(Array.from(out)).toEqual([10 / 32768, 20 / 32768, 30 / 32768]);
  });
});

// effectivePreviewOffset is the rule Sample.svelte hands to
// previewSample so host preview pitch-matches what the device
// will play. Pure function, three input cases, three expected
// outputs — locked here so a regression in the math wouldn't
// silently shift everyone's host preview.
describe('effectivePreviewOffset — best-of-both-worlds pitch math', () => {
  it('returns 0 for purely-local samples (recorded pitch)', () => {
    // Imported audio that's never touched the device — the user
    // wants to hear it as recorded. Even with a wildly different
    // mapping note hypothetically supplied, source='local' wins.
    expect(effectivePreviewOffset({ source: 'local', mappingNote: 36 })).toBe(0);
    expect(effectivePreviewOffset({ source: 'local' })).toBe(0);
  });

  it('returns 0 for device samples without a discovered mapping', () => {
    // No program currently maps this sample → no keygroup-induced
    // pitch shift would occur on a MIDI trigger anyway → host should
    // play at recorded pitch. The user can switch to device preview
    // to see "Will be silent — no mapping" instead.
    expect(effectivePreviewOffset({ source: 'device' })).toBe(0);
    expect(effectivePreviewOffset({ source: 'device', mappingNote: undefined })).toBe(0);
  });

  it('returns (note - 60) for device samples with a mapping below C3', () => {
    // Kick drum convention: mapped to MIDI 36 (C1). Device shifts
    // playback by (36 - NominalPitchAsMIDI) semitones. For NP=960
    // (C3=60), that's -24. Host needs to match: offset = 36 - 60.
    expect(effectivePreviewOffset({ source: 'device', mappingNote: 36 })).toBe(-24);
  });

  it('returns (note - 60) for device samples with a mapping above C3', () => {
    // Treble keygroup at MIDI 72 (C4). Device shifts +12; host
    // matches.
    expect(effectivePreviewOffset({ source: 'device', mappingNote: 72 })).toBe(12);
  });

  it('returns 0 for device samples mapped exactly to C3 (60)', () => {
    // Trivial case but worth pinning — no offset needed, host's
    // tune-only behaviour is correct.
    expect(effectivePreviewOffset({ source: 'device', mappingNote: 60 })).toBe(0);
  });
});
