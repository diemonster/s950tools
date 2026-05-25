import { describe, it, expect } from 'vitest';
import { hasHostAudio, buildPingPongLoopBuffer } from './preview';
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
