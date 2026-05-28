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
    // Tune inverts NominalPitch around C3=960: a LOWER NominalPitch
    // means the device plays the sample faster (higher pitch) to
    // reach the trigger key, so positive user-facing tune
    // corresponds to a lower stored NominalPitch. Same convention
    // the CLI's --tune flag uses (see uploadSample in cmd/s950-tools).
    tune: (960 - (p.NominalPitch ?? 960)) / 16,
    loudness: p.LoudOffset ?? 0,
    // Stash the 120-byte SPRM block for round-trip on Send.
    // Wails sends [120]byte as a number array; we mirror that.
    raw: Array.from(p.Raw ?? []),
    // Definitionally 'device' — this converter only runs after a
    // successful GetSampleParams round trip. originalSource matches
    // unless the existing row already had a non-device origin
    // (which a merging caller — scanMemory / ensureSampleLoaded —
    // is responsible for preserving via spread override).
    source: 'device',
    originalSource: 'device',
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

// sampleToSampleParams is the reverse direction: takes a frontend
// Sample (with its edited high-level fields) and rebuilds the
// SampleParams shape the Wails binding expects. Used by the
// per-sample "Send to S950" flow when uploading + writing SPRM in
// one call. Preserves the original Raw bytes when present so
// unmodelled fields (S950's reserved/undocumented bytes) round-trip
// safely — the backend's SetParams uses Raw as the base and only
// overwrites the high-level fields we modelled.
export function sampleToSampleParams(s: Sample): {
  Raw: number[];
  Name: string;
  TotalWords: number;
  SampleRateHz: number;
  NominalPitch: number;
  LoudOffset: number;
  ReplayMode: number;
  End: number;
  Start: number;
  LoopLength: number;
  VelXFade: number;
  Reversed: number;
} {
  return {
    Raw:          Array.from(s.raw ?? []),
    Name:         s.name.padEnd(10).slice(0, 10),
    TotalWords:   s.length,
    SampleRateHz: s.rate,
    // Mirror the inverse of the catalog converter: positive user
    // tune SUBTRACTS from NominalPitch (the device plays a sample
    // with a lower NominalPitch faster, sounding higher at the
    // trigger key). Same convention the CLI's --tune flag uses;
    // keeping them aligned means a JSON saved via the GUI uploads
    // identically via put-program.
    NominalPitch: Math.max(0, Math.min(0xFFFF, Math.round(960 - s.tune * 16))),
    LoudOffset:   s.loudness | 0,
    ReplayMode:   replayModeToByte(s.mode),
    End:          s.end,
    Start:        s.start,
    LoopLength:   s.loopLength,
    VelXFade:     s.velXfade ? 255 : 0,
    Reversed:     s.reverse ? 0x52 /* 'R' */ : 0x4E /* 'N' */,
  };
}

function replayModeToByte(m: Sample['mode']): number {
  switch (m) {
    case 'loop':      return 76; // 'L'
    case 'ping-pong': return 65; // 'A' (alternating loop)
    default:          return 79; // 'O' (one-shot)
  }
}
