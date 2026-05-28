// Wire-to-frontend converters. Lives in its own module so multiple
// stores (catalog.ts, memory.ts) can share the same SPRM → Sample
// translation without forming an import cycle (catalog.ts already
// imports from memory.ts to trigger scans after a Get).

import type { Sample } from './samples';

// sampleParamsToSample maps protocol.SampleParams (high-level decode
// of the 120-byte SPRM block) into the frontend's Sample type. The
// frontend uses string literals for the replay mode while the wire
// format uses ASCII bytes ('O' / 'L' / 'A').
export function sampleParamsToSample(p: any, slot: number): Sample {
  const total = p.TotalWords ?? 0;
  const start = p.Start ?? 0;
  const end   = p.End ?? total;
  return {
    slot,
    name: (p.Name ?? '').trim(),
    rate: p.SampleRateHz ?? 26040,
    length: total || 1,
    start,
    end,
    // S950 stores LoopLength (not loop end); LoopStart isn't a
    // separate field — the device's SPRM uses Start as the loop
    // origin. The frontend renders the loop region as
    // [loopStart..loopStart+loopLength], so we read LoopLength
    // directly and anchor loopStart to Start when present.
    loopStart: start,
    loopLength: p.LoopLength ?? 0,
    mode: replayModeFrom(p.ReplayMode),
    reverse: (p.Reversed ?? 0x4E) === 0x52, // 'R' = reverse
    velXfade: (p.VelXFade ?? 0) === 255,
    tune: ((p.NominalPitch ?? 960) - 960) / 16, // semitones from C3
    loudness: p.LoudOffset ?? 0,
    // Stash the 120-byte SPRM block for round-trip on Send.
    // Wails sends [120]byte as a number array; we mirror that.
    raw: Array.from(p.Raw ?? []),
    // Definitionally 'device' — this converter only runs after a
    // successful GetSampleParams round trip.
    source: 'device',
  } as Sample;
}

function replayModeFrom(b: number | undefined): Sample['mode'] {
  // 'O' = 79 = one-shot, 'L' = 76 = loop, 'A' = 65 = alternating
  // ('alternating loop' in the S950 manual; surfaced as ping-pong
  // throughout the UI so sample-level + slice-level terminology lines up).
  switch (b) {
    case 76: return 'loop';
    case 65: return 'ping-pong';
    default: return 'one-shot';
  }
}
