// Connection store persistence tests. The network-side flows
// (Connect / Status / catalog refresh) are validated against
// hardware, not jsdom — these tests cover only the localStorage
// round-trip for the user-selected MIDI in/out/channel so reboots
// don't force a re-pick.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';

// Mock the Wails App bindings so importing connection.ts doesn't
// fire ListPorts / Status against a missing backend.
vi.mock('../../../wailsjs/go/main/App', () => ({
  Connect:       vi.fn().mockResolvedValue(undefined),
  ConnectSerial: vi.fn().mockResolvedValue(undefined),
  Disconnect:    vi.fn().mockResolvedValue(undefined),
  ListPorts:     vi.fn().mockResolvedValue({ ins: [], outs: [], serial: [] }),
  Status:        vi.fn().mockResolvedValue({ connected: false, channel: 0 }),
}));
// Avoid the cascading catalog import — refreshCatalog isn't called
// in any of these tests, so a stub keeps the dep graph small.
vi.mock('./catalog', () => ({ refreshCatalog: vi.fn() }));

const KEY_IN      = 's950-tools.midi-in';
const KEY_OUT     = 's950-tools.midi-out';
const KEY_CHANNEL = 's950-tools.midi-channel';
const KEY_KIND    = 's950-tools.transport-kind';
const KEY_BAUD    = 's950-tools.serial-baud';

beforeEach(() => {
  localStorage.clear();
  // resetModules forces a fresh `connection.ts` per test so the
  // module-level stores re-read localStorage from scratch.
  // clearAllMocks resets the vi.fn() call history on the mocked
  // App methods (their instances survive resetModules, so calls
  // from one test would otherwise leak into the next).
  vi.resetModules();
  vi.clearAllMocks();
});

describe('connection store — initial values', () => {
  it('defaults to empty strings + channel 0 with no saved state', async () => {
    const { selectedIn, selectedOut, channel } = await import('./connection');
    expect(get(selectedIn)).toBe('');
    expect(get(selectedOut)).toBe('');
    expect(get(channel)).toBe(0);
  });

  it('reads saved port names from localStorage', async () => {
    localStorage.setItem(KEY_IN,  'MRCC Port 03');
    localStorage.setItem(KEY_OUT, 'MRCC Port 03');
    localStorage.setItem(KEY_CHANNEL, '5');
    const { selectedIn, selectedOut, channel } = await import('./connection');
    expect(get(selectedIn)).toBe('MRCC Port 03');
    expect(get(selectedOut)).toBe('MRCC Port 03');
    expect(get(channel)).toBe(5);
  });

  it('falls back to channel 0 for invalid stored values', async () => {
    // Stored channel can land here from a hand-edit or a future schema
    // change; defensive parse keeps the app booting cleanly.
    localStorage.setItem(KEY_CHANNEL, '99');
    const { channel } = await import('./connection');
    expect(get(channel)).toBe(0);
  });

  it('falls back to channel 0 when the value isn’t a number', async () => {
    localStorage.setItem(KEY_CHANNEL, 'banana');
    const { channel } = await import('./connection');
    expect(get(channel)).toBe(0);
  });

  it('rejects negative channels', async () => {
    localStorage.setItem(KEY_CHANNEL, '-1');
    const { channel } = await import('./connection');
    expect(get(channel)).toBe(0);
  });
});

describe('connection store — persistence', () => {
  it('writes selectedIn changes to localStorage', async () => {
    const { selectedIn } = await import('./connection');
    selectedIn.set('Pad 1');
    expect(localStorage.getItem(KEY_IN)).toBe('Pad 1');
  });

  it('writes selectedOut changes to localStorage', async () => {
    const { selectedOut } = await import('./connection');
    selectedOut.set('Pad 2');
    expect(localStorage.getItem(KEY_OUT)).toBe('Pad 2');
  });

  it('writes channel changes as a string', async () => {
    const { channel } = await import('./connection');
    channel.set(7);
    expect(localStorage.getItem(KEY_CHANNEL)).toBe('7');
  });

  it('clearing a pick stores an empty string (not removes the key)', async () => {
    // Setting back to '' should overwrite — important because the
    // Topbar's "— pick —" option binds to value="" and we want that
    // explicit clear to persist, not silently fall back to the
    // previous value on next launch.
    const { selectedIn } = await import('./connection');
    selectedIn.set('Port A');
    selectedIn.set('');
    expect(localStorage.getItem(KEY_IN)).toBe('');
  });
});

// ---------- Transport kind + baud ----------
// pickSerial / pickMidi are the bridge between the topbar's unified
// port chips and the connection store. The auto-mirror behavior
// (serial pick fills both directions, MIDI pick on the other side
// clears the serial mirror) is the entire point — verify it here
// rather than re-derive from inline component tests.

describe('transport kind + baud — persistence + defaults', () => {
  it('defaults to MIDI + 38400 baud with no saved state', async () => {
    const { transportKind, baud } = await import('./connection');
    expect(get(transportKind)).toBe('midi');
    expect(get(baud)).toBe(38400);
  });

  it('reads transportKind + baud from localStorage', async () => {
    localStorage.setItem(KEY_KIND, 'serial');
    localStorage.setItem(KEY_BAUD, '50000');
    const { transportKind, baud } = await import('./connection');
    expect(get(transportKind)).toBe('serial');
    expect(get(baud)).toBe(50000);
  });

  it('clamps invalid stored baud back to 38400 default', async () => {
    // SERIAL_BAUDS is a closed list (verified S950 clock-divider-
    // friendly rates); a hand-edited bogus value must not survive a
    // round-trip into the picker, which only renders valid options.
    localStorage.setItem(KEY_BAUD, '12345');
    const { baud } = await import('./connection');
    expect(get(baud)).toBe(38400);
  });

  it('treats any non-"serial" kind string as MIDI', async () => {
    localStorage.setItem(KEY_KIND, 'bogus');
    const { transportKind } = await import('./connection');
    expect(get(transportKind)).toBe('midi');
  });

  it('persists transportKind + baud changes', async () => {
    const { transportKind, baud } = await import('./connection');
    transportKind.set('serial');
    baud.set(50000);
    expect(localStorage.getItem(KEY_KIND)).toBe('serial');
    expect(localStorage.getItem(KEY_BAUD)).toBe('50000');
  });
});

describe('pickSerial — auto-mirror across in/out', () => {
  it('sets both selectedIn and selectedOut to the serial port name', async () => {
    const { pickSerial, selectedIn, selectedOut } = await import('./connection');
    pickSerial('/dev/cu.usbserial-X');
    expect(get(selectedIn)).toBe('/dev/cu.usbserial-X');
    expect(get(selectedOut)).toBe('/dev/cu.usbserial-X');
  });

  it('flips transportKind to "serial"', async () => {
    const { pickSerial, transportKind } = await import('./connection');
    transportKind.set('midi');
    pickSerial('/dev/cu.usbserial-X');
    expect(get(transportKind)).toBe('serial');
  });
});

describe('pickMidi — clears serial mirror when leaving serial', () => {
  it('setting MIDI "in" leaves MIDI "out" alone when already on MIDI', async () => {
    const { pickMidi, selectedIn, selectedOut, transportKind } = await import('./connection');
    transportKind.set('midi');
    selectedOut.set('Existing Out');
    pickMidi('in', 'New In');
    expect(get(selectedIn)).toBe('New In');
    expect(get(selectedOut)).toBe('Existing Out');
    expect(get(transportKind)).toBe('midi');
  });

  it('setting MIDI "in" clears the mirrored OUT when leaving serial', async () => {
    // While on serial both selectedIn/Out point at the same serial
    // device. Picking a MIDI input means the user is switching back
    // to MIDI — the now-stale serial path on the OUT side would be
    // wrong, so it gets cleared instead of dragging along.
    const { pickSerial, pickMidi, selectedIn, selectedOut, transportKind } = await import('./connection');
    pickSerial('/dev/cu.usbserial-X');
    pickMidi('in', 'Real MIDI Input');
    expect(get(selectedIn)).toBe('Real MIDI Input');
    expect(get(selectedOut)).toBe('');
    expect(get(transportKind)).toBe('midi');
  });

  it('setting MIDI "out" clears the mirrored IN when leaving serial', async () => {
    const { pickSerial, pickMidi, selectedIn, selectedOut, transportKind } = await import('./connection');
    pickSerial('/dev/cu.usbserial-X');
    pickMidi('out', 'Real MIDI Output');
    expect(get(selectedOut)).toBe('Real MIDI Output');
    expect(get(selectedIn)).toBe('');
    expect(get(transportKind)).toBe('midi');
  });
});

describe('connect() — dispatches based on transportKind', () => {
  it('calls App.Connect with selectedIn/Out/channel on MIDI', async () => {
    const App = await import('../../../wailsjs/go/main/App');
    const { connect, transportKind, selectedIn, selectedOut, channel } = await import('./connection');
    transportKind.set('midi');
    selectedIn.set('In Port');
    selectedOut.set('Out Port');
    channel.set(3);
    await connect();
    expect(App.Connect).toHaveBeenCalledWith('In Port', 'Out Port', 3);
    expect(App.ConnectSerial).not.toHaveBeenCalled();
  });

  it('calls App.ConnectSerial with selectedIn + baud on serial', async () => {
    const App = await import('../../../wailsjs/go/main/App');
    const { connect, pickSerial, baud } = await import('./connection');
    pickSerial('/dev/cu.usbserial-X');
    baud.set(50000);
    await connect();
    expect(App.ConnectSerial).toHaveBeenCalledWith('/dev/cu.usbserial-X', 50000);
    expect(App.Connect).not.toHaveBeenCalled();
  });
});

describe('refreshStatus — restores transportKind + baud from backend', () => {
  it('flips kind/baud to serial when the backend reports a serial session', async () => {
    const App = await import('../../../wailsjs/go/main/App');
    (App.Status as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      connected: true, kind: 'serial', in: '/dev/cu.x', out: '/dev/cu.x', channel: 0, baud: 50000,
    });
    const { refreshStatus, transportKind, baud } = await import('./connection');
    await refreshStatus();
    expect(get(transportKind)).toBe('serial');
    expect(get(baud)).toBe(50000);
  });

  it('flips kind back to midi when the backend reports a MIDI session', async () => {
    const App = await import('../../../wailsjs/go/main/App');
    (App.Status as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      connected: true, kind: 'midi', in: 'P1', out: 'P2', channel: 0,
    });
    const { refreshStatus, transportKind } = await import('./connection');
    transportKind.set('serial');
    await refreshStatus();
    expect(get(transportKind)).toBe('midi');
  });
});
