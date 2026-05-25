// Pure helpers for waveform path generation. The Sample tab's canvas
// renders a min/max peak envelope per pixel column, computed from
// either real imported PCM (`buildWaveformPaths`) or a stable
// synthetic shape derived from the slot index when no host audio is
// available (`buildSyntheticPaths`). Both functions return the same
// `{ top, bot }` shape so the .svelte template doesn't have to
// branch on which source produced the path.
//
// Coordinates are in a fixed 0..width × 0..2*amp space; the SVG
// viewBox scales these to the rendered size. Default values match
// the existing waveform layout (width=1000, centerY=100, amp=92).
//
// The peak-aggregation pattern — one (min, max) pair per output
// column — is the same trick Ableton + REAPER use to keep waveforms
// detailed at any zoom level without re-streaming the audio.

export type WaveformPaths = { top: string; bot: string };

const PCM_MAX = 32768;

// buildWaveformPaths walks the visible window of `pcm` and produces
// two filled SVG polygons: `top` traces the per-column maxima above
// centerY, `bot` traces the minima below. When the window holds
// fewer samples than output columns, neighbouring columns reuse the
// same sample so the result degrades to a stair-step rather than
// flatlining to centerY.
export function buildWaveformPaths(
  pcm: ArrayLike<number>,
  viewStart: number,
  viewLen: number,
  width = 1000,
  centerY = 100,
  amp = 92,
): WaveformPaths {
  if (!pcm || pcm.length === 0 || viewLen <= 0) {
    return flatPaths(width, centerY);
  }
  // Clamp the window into pcm bounds so partial-overlap (e.g. user
  // dragged Start past pcm.length on a stub) renders empty cleanly.
  const start = Math.max(0, Math.floor(viewStart));
  const len   = Math.min(pcm.length - start, Math.floor(viewLen));
  if (len <= 0) return flatPaths(width, centerY);

  const top: string[] = [`M 0,${centerY}`];
  const bot: string[] = [`M 0,${centerY}`];

  for (let i = 0; i < width; i++) {
    const a = start + Math.floor((i * len) / width);
    const b = start + Math.floor(((i + 1) * len) / width);
    // Always seed mx/mn from a real sample; a==b happens when the
    // window is narrower than the path so we want THIS sample, not 0.
    const first = pcm[a] ?? 0;
    let mx = first, mn = first;
    for (let j = a + 1; j < b && j < pcm.length; j++) {
      const v = pcm[j];
      if (v > mx) mx = v;
      else if (v < mn) mn = v;
    }
    const x = i + 1;
    top.push(`L ${x},${(centerY - (mx / PCM_MAX) * amp).toFixed(1)}`);
    bot.push(`L ${x},${(centerY - (mn / PCM_MAX) * amp).toFixed(1)}`);
  }
  top.push(`L ${width},${centerY} Z`);
  bot.push(`L ${width},${centerY} Z`);
  return { top: top.join(' '), bot: bot.join(' ') };
}

// buildSyntheticPaths produces a stable, slot-derived squiggle for
// samples we know about but haven't loaded the audio for (catalog
// entry seen, SPRM fetched, PCM not yet imported). Uses a seeded
// LCG so the shape is reproducible across re-renders. Same shape as
// buildWaveformPaths so callers can switch without restructuring.
export function buildSyntheticPaths(
  slot: number,
  width = 1000,
  centerY = 100,
  amp = 92,
): WaveformPaths {
  let seed = (0x9E37 ^ (slot * 0x1ABC)) >>> 0;
  const rnd = () => {
    seed = (seed * 1103515245 + 12345) & 0x7fffffff;
    return seed / 0x7fffffff;
  };
  const N = 280;
  const top: string[] = [`M 0,${centerY}`];
  const bot: string[] = [`M 0,${centerY}`];
  for (let i = 1; i <= N; i++) {
    const t = i / N;
    let env = amp * Math.exp(-3.2 * t);
    if (t < 0.04) env *= 1 + (0.04 - t) * 8;
    const jit = 0.35 + 0.65 * rnd();
    const a = env * jit;
    const x = ((i / N) * width).toFixed(1);
    top.push(`L ${x},${(centerY - a).toFixed(1)}`);
    bot.push(`L ${x},${(centerY + a).toFixed(1)}`);
  }
  top.push(`L ${width},${centerY} Z`);
  bot.push(`L ${width},${centerY} Z`);
  return { top: top.join(' '), bot: bot.join(' ') };
}

function flatPaths(width: number, centerY: number): WaveformPaths {
  const flat = `M 0,${centerY} L ${width},${centerY} Z`;
  return { top: flat, bot: flat };
}
