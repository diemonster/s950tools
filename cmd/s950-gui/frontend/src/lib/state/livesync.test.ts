// Tests for keygroupToJSON's control_bits round-trip. The S950's
// keygroup ControlBits byte holds 6 flags (transpose-off, vel-xfade,
// vibrato-desync, one-shot, vel-release, vel-xfade-curve); the GUI
// only surfaces two of them (Const-pitch → bit 0, One-shot → bit 3).
// Edits must SURVIVE the un-exposed bits — these tests pin that.

import { describe, it, expect, vi } from 'vitest';

// Mock the Wails bindings so importing livesync (which transitively
// pulls in catalog.ts) doesn't fire real RPCs at a missing window.go
// bridge. Also stub catalog itself — its module-level subscriber on
// `programs` would call ensureProgramLoaded on every programs.set()
// in newKeygroup() and crash on the null reply.
vi.mock('../../../wailsjs/go/main/App', () => ({
  SetProgram:       vi.fn().mockResolvedValue(undefined),
  SetSampleParams:  vi.fn().mockResolvedValue(undefined),
  GetProgram:       vi.fn().mockResolvedValue(null),
  GetSampleParams:  vi.fn().mockResolvedValue(null),
  ListPorts:        vi.fn().mockResolvedValue({ ins: [], outs: [], serial: [] }),
  Status:           vi.fn().mockResolvedValue({ connected: false, channel: 0 }),
}));
vi.mock('./catalog', () => ({
  ensureProgramLoaded: vi.fn(),
  refreshCatalog:      vi.fn(),
}));

import { keygroupToJSON } from './livesync';
import { newKeygroup, type Keygroup } from './programs';

// Build a 140-byte hex string where byte 36 ("kgKBITS") encodes the
// given ControlBits value via the S950's DB codec (low 7 bits at
// byte 36, bit 7 at byte 37). Other bytes are zero-padded — the
// keygroupToJSON function only reads bytes 36..37.
function rawWithControlBits(byte: number): string {
  const lo = byte & 0x7F;
  const hi = (byte >> 7) & 0x01;
  const bytes = new Array(140).fill(0);
  bytes[36] = lo;
  bytes[37] = hi;
  return bytes.map((b) => b.toString(16).padStart(2, '0')).join('');
}

describe('keygroupToJSON — control_bits round-trip', () => {
  it('preserves un-exposed bits while overwriting UI-controlled bits', () => {
    // Start from a keygroup whose ControlBits has vel-xfade (bit 1),
    // vibrato-desync (bit 2), vel-release (bit 4), and vel-xfade-curve
    // (bit 5) set — 0b00110110 = 0x36. UI toggles for one-shot (bit 3)
    // and transpose-off (bit 0) are OFF. Expect: un-exposed bits
    // survive, UI bits stay clear.
    const kg: Keygroup = {
      ...newKeygroup(1),
      oneShot: false,
      constPitch: false,
      rawBytesHex: rawWithControlBits(0x36),
    };
    const j = keygroupToJSON(kg);
    expect(j.control_bits).toBe(0x36);
  });

  it('ORs in the One-shot bit (0x08) without disturbing other bits', () => {
    const kg: Keygroup = {
      ...newKeygroup(1),
      oneShot: true,
      constPitch: false,
      rawBytesHex: rawWithControlBits(0x36),
    };
    const j = keygroupToJSON(kg);
    expect(j.control_bits).toBe(0x36 | 0x08); // = 0x3E
  });

  it('ORs in the Const-pitch bit (0x01) without disturbing other bits', () => {
    const kg: Keygroup = {
      ...newKeygroup(1),
      oneShot: false,
      constPitch: true,
      rawBytesHex: rawWithControlBits(0x36),
    };
    const j = keygroupToJSON(kg);
    expect(j.control_bits).toBe(0x36 | 0x01); // = 0x37
  });

  it('clears UI-controlled bits when their toggles are off, even if raw had them set', () => {
    // Raw says one-shot AND transpose-off were on (0x09 | 0x36 = 0x3F).
    // Both UI toggles are now OFF — the merged byte must clear them.
    const kg: Keygroup = {
      ...newKeygroup(1),
      oneShot: false,
      constPitch: false,
      rawBytesHex: rawWithControlBits(0x3F),
    };
    const j = keygroupToJSON(kg);
    expect(j.control_bits).toBe(0x36); // 0x3F with bits 0+3 cleared
  });

  it('falls back to default 4 when rawBytesHex is empty (fresh Add Zone keygroup)', () => {
    const kg: Keygroup = {
      ...newKeygroup(1),
      oneShot: false,
      constPitch: false,
      rawBytesHex: '',
    };
    const j = keygroupToJSON(kg);
    // Default base = 4 (vibrato-desync on), no UI bits added.
    expect(j.control_bits).toBe(4);
  });

  it('falls back to default 4 when rawBytesHex is malformed', () => {
    const kg: Keygroup = {
      ...newKeygroup(1),
      oneShot: true,
      constPitch: false,
      rawBytesHex: 'not actually hex bytes',
    };
    const j = keygroupToJSON(kg);
    // Malformed → base 4, plus one-shot bit ORed in.
    expect(j.control_bits).toBe(4 | 0x08);
  });

  it('handles the high bit (>= 0x80) via the DB codec', () => {
    // ControlBits is uint8, so the high bit is theoretically reachable
    // even though no documented flag uses it. The decoder must read it
    // through byte 37's bit 0, not just byte 36.
    const kg: Keygroup = {
      ...newKeygroup(1),
      oneShot: false,
      constPitch: false,
      rawBytesHex: rawWithControlBits(0x84), // bit 7 + bit 2
    };
    const j = keygroupToJSON(kg);
    expect(j.control_bits).toBe(0x84);
  });
});
