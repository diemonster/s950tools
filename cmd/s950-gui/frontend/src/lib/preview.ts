// Web Audio preview — host-side playback of imported PCM. Never
// touches the device or MIDI; entirely safe to use freely while
// editing. Driven by the ▶ buttons in the Slices list, the
// (future) ▶ on the Sample identity strip, and spacebar.
//
// One shared AudioContext, one in-flight source at a time. Hitting
// preview while audio is playing stops the previous source first —
// matches the typical "audition" pattern.

import { writable } from 'svelte/store';
import type { Sample } from './state/samples';
import type { Slice } from './state/slicing';

// playhead is a public store the Sample-tab waveform subscribes to
// for the in-flight playback cursor. While playback is live it
// updates ~60Hz with the current source-buffer word index; on stop
// it flips back to inactive. Components that don't need the cursor
// can ignore it — store is cheap (single object set per rAF tick).
export type Playhead = {
  active: boolean;
  // Sample slot the cursor belongs to. Lets the Sample tab ignore
  // playhead updates that aren't for the row currently on screen.
  sampleSlot: number;
  // Current word index in the source PCM. NOT in slice-local coords
  // — the waveform's marker math is also in source-word space.
  currentWord: number;
};
export const playhead = writable<Playhead>({
  active: false,
  sampleSlot: -1,
  currentWord: 0,
});

// AudioContext is lazily-created — most browsers gate it on user
// gesture, and we want preview to "just work" when the user clicks ▶
// even if they never explicitly resumed the context.
let ctx: AudioContext | null = null;
let current: AudioBufferSourceNode | null = null;
// The GainNode that sits between every active source and the
// destination. Owned per playback session so a fresh fade-in /
// fade-out curve can be scheduled without conflicting with leftover
// automation from a previous run.
let currentGain: GainNode | null = null;
// Secondary sources spawned alongside `current` (e.g. the ping-pong
// pre-roll node that bridges sample-start into the looping segment).
// Tracked separately so stop() can tear all of them down at once
// without losing the meaning of "is something playing" via `current`.
const extras: AudioBufferSourceNode[] = [];

// playbackSession is the snapshot previewRegion records when it
// starts a source. The rAF tracker reads it to map elapsed
// wall-clock time → current word index, accounting for the loop
// mode and direction. Cleared in stop().
export type PlaybackSession = {
  sampleSlot: number;
  startWord: number;
  lengthWords: number;
  loopMode: 'one-shot' | 'loop' | 'ping-pong';
  loopStartWord: number;
  loopLengthWord: number;
  reverse: boolean;
  rate: number;            // playbackRate (tune-derived)
  sampleRateHz: number;    // source AudioBuffer sample rate
  startedAt: number;       // AudioContext.currentTime at source.start
};
let session: PlaybackSession | null = null;
let playheadRaf: number | null = null;

// Sub-millisecond fade applied at play-START and play-STOP only.
// Suppresses the click you get when jumping from silence to the
// sample's first non-zero value, and the click when cutting out
// mid-loop. Deliberately NOT applied at loop seams — those need to
// ring through so the user can hear a mismatched loopStart/loopEnd
// (which is real diagnostic information they'd want to fix).
const FADE_SEC = 0.002;

// S950 loudness is ±50 in 0.375 dB steps (see protocol.program.go's
// SoftLoudness comment), so map directly to a linear gain factor.
// Positive values can exceed unity — the device clips there too, so
// matching that behaviour keeps preview faithful.
function loudnessToGain(loudness: number): number {
  return Math.pow(10, (loudness * 0.375) / 20);
}

// S950 tune is stored in 1/16-semitone units but the UI keeps it in
// semitones (with optional fractional cents). Web Audio's
// playbackRate scales both pitch and duration, which is exactly how
// the device's pitched playback works (no separate time-stretch).
function tuneToRate(tune: number): number {
  return Math.pow(2, tune / 12);
}

// effectivePreviewOffset reconstructs the pitch shift the S950's
// playback engine would apply for a given trigger key, so host
// preview can match what the device will actually play. Best-of-
// both-worlds rule:
//
//   • Local samples (source === 'local') OR device samples without
//     a discovered keygroup mapping → offset = 0. The user hears
//     the sample at its recorded pitch, tweaked only by tune.
//     "Listen to what you imported."
//
//   • Device samples mapped to a SINGLE KEY (lowKey === highKey,
//     the sliced-kit / drum-map shape) → offset = (note - 60).
//     The device shifts by (trigger_note - NominalPitch_as_MIDI) =
//     (trigger_note - (60 - tune)) = (trigger_note - 60) + tune.
//     Tune is already added inside previewRegion, so we hand back
//     just the (trigger_note - 60) half here. "Listen to what the
//     device will play."
//
//   • Device samples mapped across a key RANGE → offset = 0. A
//     ranged keygroup plays a different pitch on every key, so
//     there is no single "what the device will play" — guessing
//     the range midpoint (the old behaviour) made host preview of
//     Translator-/floppy-built kits play several semitones sharp
//     for no reason the user could see. Recorded pitch is the only
//     honest answer.
//
//   • CONSTANT-PITCH keygroups (the S950's "transpose off"
//     ControlBits flag) → offset = the layer's transpose alone.
//     The device plays a const-pitch keygroup at recorded pitch
//     (plus any fixed per-layer transpose) on every key — that's
//     how slice kits avoid chipmunking — so the key-tracking
//     (note - 60) term never applies.
//
//   • Pitch-tracking SINGLE-KEY keygroups → (note - 60) plus the
//     layer's transpose. The transpose term matters: the other
//     standard slice-kit shape is tracking zones with a
//     compensating per-keygroup transpose (key 73 zone carries
//     -13 st), which nets to recorded pitch on the device. Host
//     preview must compute the same sum, not just the tracking
//     half.
//
// Exported so Sample.svelte can hand the right value to
// previewSample without recomputing the rule at every call site,
// AND so the unit test can lock the contract in.
export function effectivePreviewOffset(args: {
  source: 'device' | 'local';
  mappingNote?: number;    // MIDI note, undefined when no mapping found
  mappingLowKey?: number;  // keygroup range — tracking term only applies
  mappingHighKey?: number; // when low === high (unambiguous single-key map)
  mappingConstPitch?: boolean; // keygroup transpose-off flag
  mappingTranspose?: number;   // matched layer's transpose, semitones
}): number {
  if (args.source !== 'device') return 0;
  if (args.mappingNote === undefined) return 0;
  const transpose = args.mappingTranspose ?? 0;
  if (args.mappingConstPitch) return transpose;
  if (
    args.mappingLowKey === undefined ||
    args.mappingHighKey === undefined ||
    args.mappingLowKey !== args.mappingHighKey
  ) {
    return 0;
  }
  return (args.mappingNote - 60) + transpose;
}

// normalizeLoopRegion resolves the degenerate "looping replay mode
// with an empty loop region" config into something both the audio
// graph and the playhead model can execute identically: loop the
// whole play region. One-shot mode and well-formed loop regions pass
// through untouched. Pure — exported for unit tests, because the
// failure mode it prevents (cursor parked at 0%/100% where the 2px
// line is clipped by overflow:hidden while audio keeps playing) is
// invisible in code review and very visible to users.
export function normalizeLoopRegion(
  loopMode: 'one-shot' | 'loop' | 'ping-pong',
  loopStartWord: number,
  loopLengthWord: number,
  lengthWords: number,
): { loopStartWord: number; loopLengthWord: number } {
  if (loopMode !== 'one-shot' && loopLengthWord <= 0) {
    return { loopStartWord: 0, loopLengthWord: lengthWords };
  }
  return { loopStartWord, loopLengthWord };
}

function makeFadeInGain(ac: AudioContext, target: number): GainNode {
  const gain = ac.createGain();
  const now = ac.currentTime;
  gain.gain.setValueAtTime(0, now);
  gain.gain.linearRampToValueAtTime(target, now + FADE_SEC);
  gain.connect(ac.destination);
  return gain;
}

// Schedules a linear ramp down to silence on the gain node, starting
// at `when` and ending FADE_SEC later. setValueAtTime pins the
// current gain value so the ramp begins from wherever automation
// happens to be — important if a previous ramp is mid-flight.
function scheduleFadeOutAt(gain: GainNode, ac: AudioContext, when: number) {
  const startVal = gain.gain.value;
  gain.gain.setValueAtTime(startVal, Math.max(when, ac.currentTime));
  gain.gain.linearRampToValueAtTime(0, Math.max(when, ac.currentTime) + FADE_SEC);
}

function audioContext(): AudioContext {
  if (!ctx) {
    ctx = new (window.AudioContext || (window as any).webkitAudioContext)();
  }
  if (ctx.state === 'suspended') {
    void ctx.resume();
  }
  return ctx;
}

// Cache decoded AudioBuffers per sample slot so we don't re-allocate
// + re-fill a Float32Array every time the user clicks ▶ on a slice.
// Invalidated when the sample's PCM changes (different identity).
const bufferCache = new Map<number, { pcm: number[]; rate: number; buf: AudioBuffer }>();

// zohRenderPCM renders source-rate int16 PCM into a destination-rate
// Float32 buffer using nearest-neighbor (zero-order-hold) when
// upsampling. The S950 has no anti-aliasing filter on playback —
// it just holds each sample's value until the next clock tick —
// so the host preview needs to do the same to sound like the
// device. Without this, Web Audio would sinc-interpolate the
// source PCM into the AudioContext rate and we'd lose the
// characteristic stair-step / aliasing of low-rate samples.
//
// When fromRate >= toRate the copy is 1:1 — the caller creates the
// AudioBuffer at the SOURCE rate in that case (see bufferFor), so
// the browser's own resampler performs the downsample at play time
// against a correctly-labeled buffer. (Historic bug: this branch
// used to feed a 48 kHz sample into a buffer labeled at the
// AudioContext rate, which silently played it slow/flat and broke
// the word↔seconds math downstream.)
export function zohRenderPCM(pcm: number[], fromRate: number, toRate: number, out: Float32Array): void {
  if (pcm.length === 0) return; // empty input → silent buffer (already zeroed)
  if (fromRate >= toRate) {
    // Pass-through 1:1 (or browser handles the downsample on play).
    const n = Math.min(out.length, pcm.length);
    for (let i = 0; i < n; i++) out[i] = pcm[i] / 32768;
    return;
  }
  // Nearest-neighbor upsample. Each output frame indexes back into
  // the source via floor(i * fromRate / toRate). Multiple consecutive
  // outputs landing on the same source sample = the held value,
  // which is exactly what the S950's DAC does. Clamp the index to
  // the last source frame — Math.floor rounding plus the dstFrames
  // ceil() in the caller can push srcIdx one past the end on the
  // very last output frame, which would otherwise read `undefined`
  // and produce NaN in the AudioBuffer (audible as clicks).
  const lastSrc = pcm.length - 1;
  for (let i = 0; i < out.length; i++) {
    let srcIdx = Math.floor((i * fromRate) / toRate);
    if (srcIdx > lastSrc) srcIdx = lastSrc;
    out[i] = pcm[srcIdx] / 32768;
  }
}

// bufferParamsFor decides the AudioBuffer's frame count and declared
// sample rate for a given (source length, source rate, context rate).
// Pure — exported for unit tests, since the invariant it guards is
// subtle and was silently broken once before:
//
//   INVARIANT: source word w must live at buffer time w / srcRate.
//
// Two cases keep that true:
//   • srcRate < ctxRate  → ZOH-expand into a ctxRate buffer. Word w
//     lands at frame w·ctxRate/srcRate = time w/srcRate. The
//     expansion (sample-and-hold) is deliberate: it preserves the
//     S950's unfiltered stair-step character that the browser's
//     high-quality resampler would smooth away.
//   • srcRate ≥ ctxRate  → 1:1 copy into a buffer DECLARED AT THE
//     SOURCE RATE. Word w is frame w = time w/srcRate; the browser
//     downsamples at play time. (Labeling this buffer at ctxRate —
//     the old behaviour — played 48 kHz samples slow/flat and made
//     all the words-to-seconds math address the wrong frames,
//     truncating the tail of the region.)
export function bufferParamsFor(
  srcLen: number,
  srcRate: number,
  ctxRate: number,
): { frames: number; rate: number } {
  if (srcRate < ctxRate) {
    return { frames: Math.ceil((srcLen * ctxRate) / srcRate), rate: ctxRate };
  }
  return { frames: srcLen, rate: srcRate };
}

function bufferFor(s: Sample): AudioBuffer | null {
  if (!s.pcm || s.pcm.length === 0) return null;
  const cached = bufferCache.get(s.slot);
  // Compare by identity (Wails returns a new array each fetch); ===
  // catches the "user re-imported" case so we rebuild the buffer.
  if (cached && cached.pcm === s.pcm && cached.rate === s.rate) {
    return cached.buf;
  }
  const ac = audioContext();
  const { frames, rate } = bufferParamsFor(s.pcm.length, s.rate, ac.sampleRate);
  const buf = ac.createBuffer(1, frames, rate);
  zohRenderPCM(s.pcm, s.rate, rate, buf.getChannelData(0));
  bufferCache.set(s.slot, { pcm: s.pcm, rate: s.rate, buf });
  return buf;
}

// buildReversedRegion synthesises a back-to-front copy of one sample
// range as a fresh AudioBuffer. Web Audio's playbackRate must be > 0,
// so reverse playback is implemented by reversing the PCM up front
// and playing the result forward. Built per preview call rather than
// cached — the range can change with each click (slice vs sample,
// edited Start/End) so a cache key would have to include them.
//
// Like bufferFor, we render at the AudioContext's native rate via
// zero-order-hold expansion so a low-rate (lo-fi) sample doesn't
// get smoothed by the browser's built-in resampler on playback.
function buildReversedRegion(
  s: Sample,
  startWord: number,
  lengthWords: number,
  ac: AudioContext,
): AudioBuffer | null {
  if (!s.pcm || s.pcm.length === 0) return null;
  const total = s.pcm.length;
  const begin = Math.max(0, Math.min(total, startWord));
  const len   = Math.max(1, Math.min(lengthWords, total - begin));
  // Reverse first in source space — a contiguous int16 slice — then
  // ZOH-expand to destination rate. Order doesn't matter mathematically
  // (the two operations commute on nearest-neighbor expansion), and
  // building the reverse buffer first keeps the helper one-shot.
  const reversed: number[] = new Array(len);
  for (let i = 0; i < len; i++) {
    reversed[i] = s.pcm[begin + len - 1 - i];
  }
  const { frames, rate } = bufferParamsFor(len, s.rate, ac.sampleRate);
  const buf = ac.createBuffer(1, frames, rate);
  zohRenderPCM(reversed, s.rate, rate, buf.getChannelData(0));
  return buf;
}

// stop tears down the in-flight source(s). Calling preview again
// does this automatically — exposed so the spacebar handler can
// toggle playback (second press while audio is in flight should
// stop). Rather than cutting to silence (which clicks), we ramp the
// shared GainNode to 0 over FADE_SEC and schedule each source's
// stop() at the end of the ramp. The `current` / `currentGain` /
// `extras` refs clear synchronously so isPlaying() reports false
// immediately and a subsequent preview can start fresh.
export function stop() {
  if (currentGain) {
    const ac = audioContext();
    const fadeStart = ac.currentTime;
    const stopAt    = fadeStart + FADE_SEC;
    scheduleFadeOutAt(currentGain, ac, fadeStart);
    if (current) {
      try { current.stop(stopAt); } catch {}
    }
    for (const s of extras) {
      try { s.stop(stopAt); } catch {}
    }
    // Tidy up the graph once the fade has played out — disconnect on
    // each source's `ended` event, plus a setTimeout fallback for
    // the gain (no native end event). Leaking briefly is harmless
    // (GC takes them) but explicit disconnect keeps the audio graph
    // tidy across many preview clicks.
    const g = currentGain;
    if (current) {
      const c = current;
      c.addEventListener('ended', () => { try { c.disconnect(); } catch {} });
    }
    for (const s of extras) {
      s.addEventListener('ended', () => { try { s.disconnect(); } catch {} });
    }
    setTimeout(() => { try { g.disconnect(); } catch {} }, FADE_SEC * 1000 + 20);
  }
  current = null;
  currentGain = null;
  extras.length = 0;
  stopPlayheadTracker();
}

// isPlaying lets the UI toggle preview on/off with one shortcut:
// pressing space again while a loop is running should stop, not
// stack a second source on top.
export function isPlaying(): boolean {
  return current !== null;
}

// currentWordFor maps elapsed wall-clock time to a position in the
// source PCM, honouring the session's loop mode + direction. The
// playhead UI cursor reads this every animation frame; accuracy
// matters most at loop boundaries (a slow tick at the seam looks
// like the cursor "snags"). Returned word is clamped to the
// playback range — callers can render it directly. Pure — exported
// for unit tests (sessions are plain objects).
export function currentWordFor(s: PlaybackSession, elapsedSec: number): number {
  const wordsAdvanced = elapsedSec * s.rate * s.sampleRateHz;

  if (s.reverse) {
    // Reverse playback synthesises a reversed buffer of the play
    // range and plays it forward — so in source-word space, the
    // position decreases from end as time advances. Loop modes
    // with reverse are an unusual combo on the device; we just
    // clamp at startWord rather than try to oscillate.
    const endWord = s.startWord + s.lengthWords;
    return Math.max(s.startWord, endWord - wordsAdvanced);
  }

  switch (s.loopMode) {
    case 'one-shot': {
      const pos = s.startWord + wordsAdvanced;
      return Math.min(s.startWord + s.lengthWords, pos);
    }
    case 'loop': {
      // Native Web Audio loop: source plays from startWord, then
      // wraps to loopStart when it crosses loopEnd. Mirror that
      // here so the cursor matches what the user hears.
      if (s.loopLengthWord <= 0) {
        return Math.min(s.startWord + s.lengthWords, s.startWord + wordsAdvanced);
      }
      const loopStart = s.startWord + s.loopStartWord;
      const loopEnd = loopStart + s.loopLengthWord;
      const pos = s.startWord + wordsAdvanced;
      if (pos <= loopEnd) return pos;
      const overflow = (pos - loopEnd) % s.loopLengthWord;
      return loopStart + overflow;
    }
    case 'ping-pong': {
      // previewRegion's ping-pong is pre-roll (startWord → loopEnd)
      // then a stitched [reverse(loop), forward(loop)] buffer on
      // loop. The visual position oscillates inside the loop region
      // after the pre-roll: triangle wave with period 2*loopLength.
      const loopStart = s.startWord + s.loopStartWord;
      const loopEnd = loopStart + s.loopLengthWord;
      const preRollWords = loopEnd - s.startWord;
      if (s.loopLengthWord <= 0 || wordsAdvanced <= preRollWords) {
        return Math.min(loopEnd, s.startWord + wordsAdvanced);
      }
      const inLoop = wordsAdvanced - preRollWords;
      const period = 2 * s.loopLengthWord;
      const phase = inLoop % period;
      if (phase < s.loopLengthWord) {
        // Reverse half: loopEnd → loopStart
        return loopEnd - phase;
      }
      // Forward half: loopStart → loopEnd
      return loopStart + (phase - s.loopLengthWord);
    }
  }
}

// startPlayheadTracker records the new session and kicks off the
// rAF loop that drives the `playhead` store. Replaces any prior
// session — only one preview can be in flight at a time so the
// loop is single-shot.
function startPlayheadTracker(s: PlaybackSession) {
  session = s;
  if (playheadRaf !== null) cancelAnimationFrame(playheadRaf);
  const tick = () => {
    if (!session) return;
    const elapsed = audioContext().currentTime - session.startedAt;
    const word = currentWordFor(session, Math.max(0, elapsed));
    playhead.set({
      active: true,
      sampleSlot: session.sampleSlot,
      currentWord: word,
    });
    playheadRaf = requestAnimationFrame(tick);
  };
  playheadRaf = requestAnimationFrame(tick);
}

function stopPlayheadTracker() {
  if (playheadRaf !== null) cancelAnimationFrame(playheadRaf);
  playheadRaf = null;
  session = null;
  playhead.set({ active: false, sampleSlot: -1, currentWord: 0 });
}

// previewRegion plays [startWord..startWord+lengthWords] of the
// sample's PCM through the user's speakers. If loop info is given
// it's applied via Web Audio's native loop fields; ping-pong isn't
// natively supported so it falls back to forward-loop with a
// console note.
//
// Returns true if playback started, false if there's no host-side
// audio (sample wasn't imported, or the .pcm field is empty).
export function previewRegion(
  s: Sample,
  startWord: number,
  lengthWords: number,
  loopMode: 'one-shot' | 'loop' | 'ping-pong' = 'one-shot',
  loopStartWord = 0,
  loopLengthWord = 0,
  reverse = false,
  // pitchOffsetSemitones is an extra semitone shift the caller adds
  // on top of the sample's own tune. Used by the Sample tab to mirror
  // the device's keygroup-mapping pitch math: when a device-loaded
  // sample is mapped to (say) key 36, triggering at 36 on the device
  // pitches it (36 - 60) = -24 semitones from its NominalPitch. Host
  // preview needs the same offset to sound like what the device will
  // actually play. Defaults to 0 so all existing call sites (slice
  // preview, spacebar) keep playing at the recorded pitch + the
  // sample's tune.
  pitchOffsetSemitones = 0,
): boolean {
  stop();
  const ac = audioContext();

  // Normalize a degenerate loop config up front: REPLAY mode LOOP /
  // PING-PONG with LoopLength 0. Common in practice — slices and
  // one-shot drums carry LoopLength 0 in their SPRM, and flipping
  // the MODE button doesn't invent a loop region. Without this the
  // audio and the playhead model diverge: Web Audio treats
  // loopEnd <= loopStart as "loop the whole buffer" (audio keeps
  // playing) while currentWordFor clamps the cursor at the region
  // end (loop — parks at 100%, clipped off-screen) or pins it at
  // the region start (ping-pong falls through to a no-loop source
  // — parks at 0%, also clipped). Treating "loop with no region"
  // as "loop the whole play region" keeps sound and cursor honest
  // and matches what the user meant by pressing LOOP.
  const normalized = normalizeLoopRegion(loopMode, loopStartWord, loopLengthWord, lengthWords);
  loopStartWord = normalized.loopStartWord;
  loopLengthWord = normalized.loopLengthWord;

  // For reverse playback, build a one-off reversed buffer over the
  // play region and treat the source coordinates as if start=0.
  // loopStartWord (a forward offset from the region start) flips to
  // count from the back of the (now-reversed) region. The rest of
  // the function uses these effective values uniformly so the
  // ping-pong / one-shot / loop branches don't need their own
  // reverse-aware paths.
  let buf: AudioBuffer | null;
  let effStartWord: number;
  let effLoopStartWord: number;
  if (reverse) {
    buf = buildReversedRegion(s, startWord, lengthWords, ac);
    effStartWord = 0;
    effLoopStartWord = Math.max(0, lengthWords - (loopStartWord + loopLengthWord));
  } else {
    buf = bufferFor(s);
    effStartWord = startWord;
    effLoopStartWord = loopStartWord;
  }
  if (!buf) return false;

  const startSec  = effStartWord  / s.rate;
  const lenSec    = lengthWords / s.rate;
  const loopStartAbs = startSec + effLoopStartWord / s.rate;
  const loopEndAbs   = startSec + (effLoopStartWord + loopLengthWord) / s.rate;

  const rate    = tuneToRate((s.tune ?? 0) + pitchOffsetSemitones);
  const gainAmt = loudnessToGain(s.loudness ?? 0);

  // Build the per-session gain node now so every source we wire
  // below routes through it. Fade-in is scheduled inside the
  // factory; fade-out is scheduled below or in stop().
  const gain = makeFadeInGain(ac, gainAmt);

  // ---------- Ping-pong ----------
  // Web Audio's native loop is forward-only. We synthesise ping-pong
  // by building a stitched buffer of [reverse(loop), forward(loop)]
  // and looping that. The reverse → forward boundary is mathematically
  // continuous (both halves meet at the loopStart sample), so the
  // loop seam is inaudible.
  if (loopMode === 'ping-pong' && loopLengthWord > 0) {
    // Pre-roll: play normally from startWord up to the end of the
    // loop region (the standard ping-pong entry), no loop.
    const preRoll = ac.createBufferSource();
    preRoll.buffer = buf;
    preRoll.playbackRate.value = rate;
    preRoll.connect(gain);
    const preRollEnd = loopEndAbs;       // absolute seconds in source buffer
    const preRollDur = preRollEnd - startSec;
    preRoll.start(0, startSec, preRollDur);

    // Loop buffer: reverse half then forward half, both covering the
    // loop region. Looping this buffer produces alternating
    // backward/forward playback that bounces off loopStart and loopEnd.
    // See buildPingPongLoopBuffer() for the math.
    //
    // Frame indices and the stitched buffer's rate must both use the
    // SOURCE BUFFER's declared rate (buf.sampleRate), not s.rate —
    // they differ whenever bufferParamsFor ZOH-expanded a low-rate
    // sample to the context rate. Indexing with s.rate extracted the
    // wrong slice of frames and the s.rate-labeled pingBuf played it
    // at the wrong speed.
    const srcArr = buf.getChannelData(0);
    const bufRate = buf.sampleRate;
    const startIdx = Math.max(0, Math.floor(loopStartAbs * bufRate));
    const endIdx   = Math.min(srcArr.length, Math.floor(loopEndAbs * bufRate));
    const pingData = buildPingPongLoopBuffer(srcArr, startIdx, endIdx);
    const pingBuf  = ac.createBuffer(1, pingData.length, bufRate);
    pingBuf.getChannelData(0).set(pingData);

    const loopSrc = ac.createBufferSource();
    loopSrc.buffer = pingBuf;
    loopSrc.loop = true;
    loopSrc.playbackRate.value = rate;
    loopSrc.connect(gain);
    // playbackRate scales the pre-roll's real-time duration, so the
    // loop must start later/earlier than the source-buffer duration
    // would suggest. preRollDur is in source seconds; divide by rate
    // to get wall-clock seconds.
    loopSrc.start(ac.currentTime + preRollDur / rate);

    extras.push(preRoll);
    current = loopSrc;
    currentGain = gain;
    startPlayheadTracker({
      sampleSlot: s.slot,
      startWord, lengthWords, loopMode,
      loopStartWord, loopLengthWord,
      reverse,
      rate,
      sampleRateHz: s.rate,
      startedAt: ac.currentTime,
    });
    return true;
  }

  // ---------- One-shot + forward loop ----------
  const src = ac.createBufferSource();
  src.buffer = buf;
  src.playbackRate.value = rate;

  if (loopMode === 'loop') {
    src.loop = true;
    // loopStart/loopEnd are absolute seconds in the buffer (not
    // relative to the source's start parameter), per spec.
    src.loopStart = loopStartAbs;
    src.loopEnd   = loopEndAbs;
  }

  src.connect(gain);
  src.start(0, startSec, loopMode === 'one-shot' ? lenSec : undefined);

  // One-shot has a known end time — schedule the fade-out so the
  // tail of the clip ramps to silence instead of cutting at lenSec.
  // Clamp to half the clip length so very short samples don't
  // double-fade (in→out overlap = audible pop). For ≤2*FADE_SEC
  // clips, skip the out-fade entirely. lenSec is in source-buffer
  // seconds; divide by rate to convert to wall-clock seconds for
  // the AudioContext schedule.
  const realLenSec = lenSec / rate;
  if (loopMode === 'one-shot' && realLenSec > 2 * FADE_SEC) {
    const endTime = ac.currentTime + realLenSec;
    scheduleFadeOutAt(gain, ac, endTime - FADE_SEC);
  }

  current = src;
  currentGain = gain;
  startPlayheadTracker({
    sampleSlot: s.slot,
    startWord, lengthWords, loopMode,
    loopStartWord, loopLengthWord,
    reverse,
    rate,
    sampleRateHz: s.rate,
    startedAt: ac.currentTime,
  });
  // Auto-clear after the play window so subsequent stop() is a no-op.
  if (loopMode === 'one-shot') {
    src.onended = () => {
      if (current === src) { current = null; currentGain = null; }
      try { src.disconnect(); } catch {}
      try { gain.disconnect(); } catch {}
      // The rAF loop would otherwise keep ticking until the next
      // user action; clear the playhead at the natural end of a
      // one-shot too.
      stopPlayheadTracker();
    };
  }
  return true;
}

// previewSample plays the active region of a sample (Start..End)
// respecting its replay mode + reverse flag. Returns false when no
// host-side audio is available.
//
// pitchOffsetSemitones (default 0) is the extra semitone shift the
// caller wants on top of the sample's own tune. Sample.svelte uses
// this to mirror the device's keygroup-mapping pitch math — see
// the doc on previewRegion's same-named parameter for details.
export function previewSample(s: Sample, pitchOffsetSemitones = 0): boolean {
  if (!s.pcm) return false;
  const len = Math.max(1, s.end - s.start);
  const loopMode = s.mode;
  // Loop relative to slice start = (Start..LoopLength) since the S950
  // anchors loops at the sample's start point.
  return previewRegion(s, s.start, len, loopMode, 0, s.loopLength, s.reverse, pitchOffsetSemitones);
}

// previewSlice plays one slice of a sample (which is itself part of
// the imported source). The slice's loop config is applied within
// the slice's local window.
export function previewSlice(s: Sample, sl: Slice): boolean {
  if (!s.pcm) return false;
  return previewRegion(s, sl.start, sl.length, sl.loopMode, sl.loopStart, sl.loopLength);
}

// hasHostAudio is the UI guard for the ▶ button: dim it when the
// sample has no in-memory PCM (i.e. wasn't imported in this session).
export function hasHostAudio(s: Sample | undefined): boolean {
  return Boolean(s && s.pcm && s.pcm.length > 0);
}

// buildPingPongLoopBuffer constructs the audio segment that
// previewRegion loops to synthesise ping-pong playback. Web Audio
// has no native ping-pong, so we hand-stitch a [reverse, forward]
// buffer and play it with loop=true.
//
// Inputs: source samples and the loop region [startIdx, endIdx)
//   (endIdx exclusive). Pre-roll plays [..., endIdx-1] forward
//   before this buffer takes over.
//
// Output: a Float32Array of length 2*segLen where
//   • [0..segLen)        is the reverse traversal (endIdx-1 → startIdx)
//   • [segLen..2*segLen) is the forward traversal (startIdx → endIdx-1)
//
// Bounce points (reverse→forward inside the buffer, and end→start
// across the loop seam) repeat one sample each — at the natural
// zero-velocity turnaround that's perceptually fine. The shape is
// locked in by tests so we don't regress this on a casual refactor.
export function buildPingPongLoopBuffer(
  srcArr: Float32Array | ArrayLike<number>,
  startIdx: number,
  endIdx: number,
): Float32Array {
  const segLen = Math.max(1, endIdx - startIdx);
  const out = new Float32Array(segLen * 2);
  for (let i = 0; i < segLen; i++) out[i] = srcArr[endIdx - 1 - i];
  for (let i = 0; i < segLen; i++) out[segLen + i] = srcArr[startIdx + i];
  return out;
}
