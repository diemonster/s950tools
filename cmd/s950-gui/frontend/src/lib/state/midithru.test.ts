// Tests for the MIDI-thru store — the Wails bindings are mocked so
// the start/stop round-trips and the error path run in-process.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';

vi.mock('../../../wailsjs/go/main/App', () => ({
  MidiThruCaps:   vi.fn().mockResolvedValue({
    virtualSupported: true, virtualPortName: 'S950 RS-232 (s950-tools)', hint: '',
  }),
  MidiThruStart:  vi.fn(),
  MidiThruStatus: vi.fn().mockResolvedValue({ active: false }),
  MidiThruStop:   vi.fn().mockResolvedValue({ active: false }),
}));

import * as App from '../../../wailsjs/go/main/App';
import {
  midiThru, midiThruError, startThru, stopThru, initMidiThruEvents,
  syncThruStatus, thruPref, setThruPref, autoStartThru, thruCaps,
  type MidiThruState,
} from './midithru';

const ACTIVE: MidiThruState = {
  active: true, source: 'MRCC Port 7', virtual: false, forwarded: 0, dropped: 0,
};

beforeEach(() => {
  vi.clearAllMocks();
  midiThru.set({ active: false, source: '', virtual: false, forwarded: 0, dropped: 0 });
  midiThruError.set('');
  thruCaps.set({ virtualSupported: true, virtualPortName: 'S950 RS-232 (s950-tools)', hint: '' });
  ((App as any).MidiThruStop as ReturnType<typeof vi.fn>)
    .mockResolvedValue({ active: false });
  // mockResolvedValue survives clearAllMocks — re-install the
  // supported-platform default so the Windows tests can't leak.
  ((App as any).MidiThruCaps as ReturnType<typeof vi.fn>).mockResolvedValue({
    virtualSupported: true, virtualPortName: 'S950 RS-232 (s950-tools)', hint: '',
  });
});

describe('startThru', () => {
  it('passes port/virtual through and adopts the returned state', async () => {
    ((App as any).MidiThruStart as ReturnType<typeof vi.fn>).mockResolvedValue(ACTIVE);
    const ok = await startThru('MRCC Port 7', false);
    expect(ok).toBe(true);
    expect((App as any).MidiThruStart).toHaveBeenCalledWith('MRCC Port 7', false);
    expect(get(midiThru)).toEqual(ACTIVE);
    expect(get(midiThruError)).toBe('');
  });

  it('virtual mode passes virtual=true with empty port', async () => {
    ((App as any).MidiThruStart as ReturnType<typeof vi.fn>)
      .mockResolvedValue({ ...ACTIVE, source: 'S950 RS-232 (s950-tools)', virtual: true });
    await startThru('', true);
    expect((App as any).MidiThruStart).toHaveBeenCalledWith('', true);
    expect(get(midiThru).virtual).toBe(true);
  });

  it('surfaces binding errors without flipping active', async () => {
    ((App as any).MidiThruStart as ReturnType<typeof vi.fn>)
      .mockRejectedValue(new Error('MIDI thru requires an RS-232 session'));
    const ok = await startThru('X', false);
    expect(ok).toBe(false);
    expect(get(midiThru).active).toBe(false);
    expect(get(midiThruError)).toContain('RS-232');
  });
});

describe('stopThru', () => {
  it('calls the binding and resets state', async () => {
    midiThru.set(ACTIVE);
    await stopThru();
    expect((App as any).MidiThruStop).toHaveBeenCalled();
    expect(get(midiThru).active).toBe(false);
  });

  it('resets state even when the binding rejects (transport already gone)', async () => {
    midiThru.set(ACTIVE);
    ((App as any).MidiThruStop as ReturnType<typeof vi.fn>)
      .mockRejectedValue(new Error('boom'));
    await stopThru();
    expect(get(midiThru).active).toBe(false);
    expect(get(midiThruError)).toContain('boom');
  });
});

describe('syncThruStatus', () => {
  it('reseeds the store from the backend after a frontend reload', async () => {
    // The backend keeps forwarding across webview reloads while the
    // store re-initialises to INACTIVE — the resync must restore the
    // chip's view so the user can still stop the session.
    ((App as any).MidiThruStatus as ReturnType<typeof vi.fn>)
      .mockResolvedValue({ ...ACTIVE, forwarded: 999 });
    await syncThruStatus();
    expect(get(midiThru).active).toBe(true);
    expect(get(midiThru).forwarded).toBe(999);
  });

  it('keeps the inactive default when the binding is unreachable', async () => {
    ((App as any).MidiThruStatus as ReturnType<typeof vi.fn>)
      .mockRejectedValue(new Error('no runtime'));
    await syncThruStatus();
    expect(get(midiThru).active).toBe(false);
  });
});

describe('thru auto-start preference', () => {
  beforeEach(() => {
    localStorage.removeItem('s950-tools.midithru');
  });

  it('defaults to virtual so a fresh install is zero-config for DAWs', () => {
    expect(thruPref()).toBe('virtual');
  });

  it('autoStartThru starts the virtual port by default', async () => {
    ((App as any).MidiThruStart as ReturnType<typeof vi.fn>)
      .mockResolvedValue({ ...ACTIVE, virtual: true });
    await autoStartThru();
    expect((App as any).MidiThruStart).toHaveBeenCalledWith('', true);
  });

  it('respects a persisted "off" — user choice survives reconnects', async () => {
    setThruPref('');
    await autoStartThru();
    expect((App as any).MidiThruStart).not.toHaveBeenCalled();
  });

  it('restores a remembered physical input', async () => {
    setThruPref('port:MRCC Port 7');
    ((App as any).MidiThruStart as ReturnType<typeof vi.fn>).mockResolvedValue(ACTIVE);
    await autoStartThru();
    expect((App as any).MidiThruStart).toHaveBeenCalledWith('MRCC Port 7', false);
  });

  it('a failed auto-start surfaces in midiThruError, not a throw', async () => {
    ((App as any).MidiThruStart as ReturnType<typeof vi.fn>)
      .mockRejectedValue(new Error('virtual MIDI ports are not supported'));
    await autoStartThru(); // must not throw
    expect(get(midiThru).active).toBe(false);
    expect(get(midiThruError)).toContain('not supported');
  });

  it('skips the virtual default silently on platforms without virtual ports (Windows)', async () => {
    // Windows: WinMM has no app-created MIDI ports. The 'virtual'
    // default must not fire a doomed start on every connect — the
    // chip shows the loopMIDI hint instead, with no error noise.
    ((App as any).MidiThruCaps as ReturnType<typeof vi.fn>).mockResolvedValue({
      virtualSupported: false,
      virtualPortName: 'S950 RS-232 (s950-tools)',
      hint: 'Install a loopback driver (e.g. loopMIDI)…',
    });
    await autoStartThru();
    expect((App as any).MidiThruStart).not.toHaveBeenCalled();
    expect(get(midiThruError)).toBe('');
    expect(get(thruCaps).virtualSupported).toBe(false);
  });

  it('still restores a remembered physical input on Windows', async () => {
    // The physical-port path (incl. loopMIDI ports) is fully
    // supported everywhere — capability gating must not block it.
    ((App as any).MidiThruCaps as ReturnType<typeof vi.fn>).mockResolvedValue({
      virtualSupported: false, virtualPortName: '', hint: 'loopMIDI…',
    });
    setThruPref('port:loopMIDI Port');
    ((App as any).MidiThruStart as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...ACTIVE, source: 'loopMIDI Port',
    });
    await autoStartThru();
    expect((App as any).MidiThruStart).toHaveBeenCalledWith('loopMIDI Port', false);
  });
});

describe('initMidiThruEvents', () => {
  it('subscribes once and mirrors backend pushes into the store', () => {
    let handler: ((st: MidiThruState) => void) | null = null;
    const eventsOn = vi.fn((name: string, cb: (st: MidiThruState) => void) => {
      expect(name).toBe('midithru:state');
      handler = cb;
    });
    initMidiThruEvents(eventsOn as any);
    initMidiThruEvents(eventsOn as any); // idempotent — second call no-ops
    expect(eventsOn).toHaveBeenCalledTimes(1);
    expect(handler).toBeTruthy();
    handler!({ ...ACTIVE, forwarded: 214 });
    expect(get(midiThru).forwarded).toBe(214);
  });
});
