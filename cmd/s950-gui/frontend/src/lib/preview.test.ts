import { describe, it, expect } from 'vitest';
import {
  hasHostAudio,
  buildPingPongLoopBuffer,
  zohRenderPCM,
  effectivePreviewOffset,
  bufferParamsFor,
  normalizeLoopRegion,
  currentWordFor,
  type PlaybackSession,
} from './preview';
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
// will play. Pure function — locked here so a regression in the
// math wouldn't silently shift everyone's host preview.
//
// The offset only applies to SINGLE-KEY keygroup mappings
// (lowKey === highKey, the sliced-kit / drum-map shape). A ranged
// keygroup plays a different pitch on every key, so there's no one
// device pitch to mirror — the old midpoint guess made host preview
// of Translator-built kits (wide-range keygroups) play several
// semitones sharp.
describe('effectivePreviewOffset — best-of-both-worlds pitch math', () => {
  it('returns 0 for purely-local samples (recorded pitch)', () => {
    // Imported audio that's never touched the device — the user
    // wants to hear it as recorded. Even with a wildly different
    // mapping note hypothetically supplied, source='local' wins.
    expect(effectivePreviewOffset({
      source: 'local', mappingNote: 36, mappingLowKey: 36, mappingHighKey: 36,
    })).toBe(0);
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

  it('returns (note - 60) for single-key mappings below C3', () => {
    // Kick drum convention: mapped to MIDI 36 (C1) only. Device
    // shifts playback by (36 - NominalPitchAsMIDI) semitones. For
    // NP=960 (C3=60), that's -24. Host needs to match.
    expect(effectivePreviewOffset({
      source: 'device', mappingNote: 36, mappingLowKey: 36, mappingHighKey: 36,
    })).toBe(-24);
  });

  it('returns (note - 60) for single-key mappings above C3', () => {
    expect(effectivePreviewOffset({
      source: 'device', mappingNote: 72, mappingLowKey: 72, mappingHighKey: 72,
    })).toBe(12);
  });

  it('returns 0 for a single-key mapping exactly at C3 (60)', () => {
    expect(effectivePreviewOffset({
      source: 'device', mappingNote: 60, mappingLowKey: 60, mappingHighKey: 60,
    })).toBe(0);
  });

  it('returns 0 for RANGED keygroups even when a midpoint note is supplied', () => {
    // Translator-/floppy-built kits typically map each sample across
    // a wide key range (e.g. 24..127, midpoint 75). The device plays
    // a different pitch per trigger key, so host preview must stay
    // at recorded pitch instead of guessing — the +15-semitone
    // sharp playback this used to cause is the regression locked
    // out here.
    expect(effectivePreviewOffset({
      source: 'device', mappingNote: 75, mappingLowKey: 24, mappingHighKey: 127,
    })).toBe(0);
    // Even a narrow 2-key range is ambiguous.
    expect(effectivePreviewOffset({
      source: 'device', mappingNote: 60, mappingLowKey: 60, mappingHighKey: 61,
    })).toBe(0);
  });

  it('returns 0 when the range is not supplied at all (defensive)', () => {
    // A caller that only knows the note can't prove single-key —
    // recorded pitch is the safe default.
    expect(effectivePreviewOffset({ source: 'device', mappingNote: 36 })).toBe(0);
  });

  it('drops the key-tracking term for CONSTANT-PITCH keygroups', () => {
    // The S950's transpose-off flag: a const-pitch keygroup plays
    // its sample at recorded pitch on EVERY key — the standard
    // slice-kit shape (one key per slice, const-pitch on, so slice
    // 18 at key 77 doesn't chipmunk on the hardware). Host preview
    // must not apply (77 - 60) = +17 semitones the device never
    // plays.
    expect(effectivePreviewOffset({
      source: 'device', mappingNote: 77, mappingLowKey: 77, mappingHighKey: 77,
      mappingConstPitch: true,
    })).toBe(0);
    // ...but the layer's fixed transpose still applies — the device
    // honours it on every key even with tracking off.
    expect(effectivePreviewOffset({
      source: 'device', mappingNote: 77, mappingLowKey: 77, mappingHighKey: 77,
      mappingConstPitch: true, mappingTranspose: -5,
    })).toBe(-5);
    // Pitch-tracking single-key mapping still gets the tracking term.
    expect(effectivePreviewOffset({
      source: 'device', mappingNote: 77, mappingLowKey: 77, mappingHighKey: 77,
      mappingConstPitch: false,
    })).toBe(17);
  });

  it('adds the layer transpose to the tracking term (compensated slice kits)', () => {
    // The OTHER standard slice-kit shape: pitch-tracking single-key
    // zones, each carrying a compensating transpose so the slice
    // plays at recorded pitch at its own trigger key. Tracking
    // (73 - 60 = +13) + transpose (-13) must net to 0 — the device
    // plays recorded pitch and host preview must match.
    expect(effectivePreviewOffset({
      source: 'device', mappingNote: 73, mappingLowKey: 73, mappingHighKey: 73,
      mappingConstPitch: false, mappingTranspose: -13,
    })).toBe(0);
    // Partial compensation nets to the residual the device plays.
    expect(effectivePreviewOffset({
      source: 'device', mappingNote: 73, mappingLowKey: 73, mappingHighKey: 73,
      mappingConstPitch: false, mappingTranspose: -1,
    })).toBe(12);
  });
});

// normalizeLoopRegion resolves "looping mode, empty loop region" —
// the config every Translator/one-shot slice lands in when the user
// flips the MODE button to LOOP or PING-PONG (SPRM LoopLength stays
// 0). Audio and playhead must agree on the interpretation.
describe('normalizeLoopRegion — degenerate loop-region defaulting', () => {
  it('expands an empty region to the whole play range for loop mode', () => {
    expect(normalizeLoopRegion('loop', 0, 0, 71111))
      .toEqual({ loopStartWord: 0, loopLengthWord: 71111 });
  });

  it('expands an empty region for ping-pong mode', () => {
    expect(normalizeLoopRegion('ping-pong', 0, 0, 4076))
      .toEqual({ loopStartWord: 0, loopLengthWord: 4076 });
  });

  it('resets a nonzero loopStart when the region is empty', () => {
    // A stale loopStart with zero length is still "no region".
    expect(normalizeLoopRegion('loop', 500, 0, 1000))
      .toEqual({ loopStartWord: 0, loopLengthWord: 1000 });
  });

  it('passes well-formed loop regions through untouched', () => {
    expect(normalizeLoopRegion('loop', 100, 200, 1000))
      .toEqual({ loopStartWord: 100, loopLengthWord: 200 });
    expect(normalizeLoopRegion('ping-pong', 0, 71111, 71111))
      .toEqual({ loopStartWord: 0, loopLengthWord: 71111 });
  });

  it('leaves one-shot mode alone even with an empty region', () => {
    // One-shot has no loop — inventing one would change the audible
    // behaviour (the source would stop honouring its duration).
    expect(normalizeLoopRegion('one-shot', 0, 0, 1000))
      .toEqual({ loopStartWord: 0, loopLengthWord: 0 });
  });
});

// currentWordFor drives the playhead cursor. These tests pin the
// regression the user reported: with a (now normalized) loop region,
// the cursor must keep wrapping instead of parking at the region end
// (loop) or start (ping-pong) where the 2px line is clipped by the
// waveform's overflow:hidden.
describe('currentWordFor — playhead position math', () => {
  // A whole-sample loop session at 48 kHz, unity playback rate —
  // the user's exact shape after normalizeLoopRegion.
  const loopSession: PlaybackSession = {
    sampleSlot: 0,
    startWord: 0,
    lengthWords: 71111,
    loopMode: 'loop',
    loopStartWord: 0,
    loopLengthWord: 71111,
    reverse: false,
    rate: 1,
    sampleRateHz: 48000,
    startedAt: 0,
  };
  const passDur = 71111 / 48000; // ≈ 1.4815 s per pass

  it('sweeps forward during the first pass', () => {
    expect(currentWordFor(loopSession, 0)).toBe(0);
    expect(currentWordFor(loopSession, passDur / 2)).toBeCloseTo(71111 / 2, 0);
  });

  it('wraps back into the loop region after the first pass (regression)', () => {
    // 1.25 passes in: cursor must be at 25% of the loop, NOT parked
    // at the end.
    const word = currentWordFor(loopSession, passDur * 1.25);
    expect(word).toBeCloseTo(71111 * 0.25, 0);
    // Ten passes in — still wrapping.
    const word10 = currentWordFor(loopSession, passDur * 10.5);
    expect(word10).toBeCloseTo(71111 * 0.5, 0);
    expect(word10).toBeLessThan(71111);
    expect(word10).toBeGreaterThan(0);
  });

  it('one-shot clamps at the region end', () => {
    const oneShot: PlaybackSession = { ...loopSession, loopMode: 'one-shot' };
    expect(currentWordFor(oneShot, passDur * 2)).toBe(71111);
  });

  it('ping-pong: pre-roll sweep, then triangle oscillation', () => {
    const pp: PlaybackSession = { ...loopSession, loopMode: 'ping-pong' };
    // Pre-roll (first pass) sweeps forward.
    expect(currentWordFor(pp, passDur * 0.5)).toBeCloseTo(71111 * 0.5, 0);
    // After the pre-roll: reverse half first (loopEnd → loopStart).
    expect(currentWordFor(pp, passDur * 1.5)).toBeCloseTo(71111 * 0.5, 0);
    // Then the forward half (loopStart → loopEnd).
    expect(currentWordFor(pp, passDur * 2.5)).toBeCloseTo(71111 * 0.5, 0);
    // Never escapes the region.
    for (const mult of [1.1, 1.9, 2.3, 3.7, 8.2]) {
      const w = currentWordFor(pp, passDur * mult);
      expect(w).toBeGreaterThanOrEqual(0);
      expect(w).toBeLessThanOrEqual(71111);
    }
  });

  it('playback rate scales cursor speed', () => {
    // +12 semitones = 2× rate: one pass completes in half the time.
    const fast: PlaybackSession = { ...loopSession, rate: 2 };
    expect(currentWordFor(fast, passDur / 2)).toBeCloseTo(71111, 0);
  });

  it('reverse sweeps from the end and clamps at the start', () => {
    const rev: PlaybackSession = { ...loopSession, loopMode: 'one-shot', reverse: true };
    expect(currentWordFor(rev, 0)).toBe(71111);
    expect(currentWordFor(rev, passDur / 2)).toBeCloseTo(71111 / 2, 0);
    expect(currentWordFor(rev, passDur * 3)).toBe(0);
  });
});

// bufferParamsFor guards the invariant that source word w lives at
// buffer time w / srcRate — every words-to-seconds conversion in
// previewRegion (start offset, duration, loop points, fade-out
// scheduling) relies on it. The bug class this locks out: a 48 kHz
// sample copied 1:1 into a 44.1 kHz-labeled buffer played ~1.5
// semitones flat AND had its tail truncated because the play window
// addressed the wrong frames.
describe('bufferParamsFor — buffer rate/length selection', () => {
  it('ZOH-expands low-rate samples into a context-rate buffer', () => {
    // 26040 Hz sample on a 48 kHz context: frames scale up, buffer
    // is at the context rate (the stair-step expansion path).
    const p = bufferParamsFor(26040, 26040, 48000);
    expect(p.rate).toBe(48000);
    expect(p.frames).toBe(Math.ceil((26040 * 48000) / 26040));
  });

  it('labels the buffer at the SOURCE rate when srcRate > ctxRate', () => {
    // 48 kHz sample on a 44.1 kHz context: 1:1 frames, buffer
    // declared at 48 kHz so the browser downsamples correctly and
    // word w stays at time w/48000.
    const p = bufferParamsFor(71112, 48000, 44100);
    expect(p.rate).toBe(48000);
    expect(p.frames).toBe(71112);
  });

  it('is 1:1 at equal rates', () => {
    const p = bufferParamsFor(1000, 48000, 48000);
    expect(p.rate).toBe(48000);
    expect(p.frames).toBe(1000);
  });

  it('keeps the invariant: word w at time w/srcRate in both branches', () => {
    const srcLen = 500;
    for (const [srcRate, ctxRate] of [[26040, 48000], [48000, 44100], [22050, 22050]] as const) {
      const { frames, rate } = bufferParamsFor(srcLen, srcRate, ctxRate);
      // Last word's buffer-time position must equal (srcLen-1)/srcRate
      // regardless of which branch was taken. In the expansion branch
      // word w sits at frame floor(w*rate/srcRate); in the 1:1 branch
      // at frame w. Both divided by `rate` give w/srcRate (±1 frame
      // of floor rounding).
      const lastWordFrame = srcRate < ctxRate
        ? Math.floor(((srcLen - 1) * rate) / srcRate)
        : srcLen - 1;
      const bufferTime = lastWordFrame / rate;
      const expected = (srcLen - 1) / srcRate;
      expect(Math.abs(bufferTime - expected)).toBeLessThan(1 / rate + 1e-9);
      expect(lastWordFrame).toBeLessThan(frames);
    }
  });
});
