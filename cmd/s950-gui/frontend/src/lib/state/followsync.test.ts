// Follow-selection gate tests. decideFollow is the pure function
// that decides whether selecting a program may send a MIDI Program
// Change to the S950 — every blocked branch matters because a wrong
// "ok" silently switches the hardware to a different program than
// the one on screen. The debounced activation flow itself talks to
// the device and is validated on hardware; here we also cover the
// persisted toggle round-trip.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';

// Mock the Wails bindings so importing followsync.ts (→ connection.ts)
// doesn't fire backend calls in jsdom.
vi.mock('../../../wailsjs/go/main/App', () => ({
  ActivateProgram: vi.fn().mockResolvedValue(undefined),
  Connect:         vi.fn().mockResolvedValue(undefined),
  ConnectSerial:   vi.fn().mockResolvedValue(undefined),
  Disconnect:      vi.fn().mockResolvedValue(undefined),
  ListPorts:       vi.fn().mockResolvedValue({ ins: [], outs: [], serial: [] }),
  Status:          vi.fn().mockResolvedValue({ connected: false, channel: 0 }),
  VerifyDevice:    vi.fn().mockResolvedValue(undefined),
}));
vi.mock('./catalog', () => ({
  refreshCatalog:      vi.fn(),
  ensureProgramLoaded: vi.fn().mockResolvedValue(undefined),
  isProgramHydrated:   vi.fn().mockReturnValue(true),
}));

import { decideFollow } from './followsync';
import { newLocalProgram, type Program } from './programs';

// Hydration oracles for the pure-gate tests: ALL_HYDRATED is the
// steady state (eager hydration finished); NONE_HYDRATED models the
// window right after a catalog refresh.
const ALL_HYDRATED  = () => true;
const NONE_HYDRATED = () => false;

// deviceProgram builds the happy-path fixture: a device-side program
// with RESPOND TO PC on and a given MIDI prog #.
function deviceProgram(slot: number, midiProg: number, over: Partial<Program> = {}): Program {
  return {
    ...newLocalProgram(slot, `PROG ${slot}`),
    source: 'device',
    respondPC: true,
    midiProg,
    ...over,
  };
}

describe('decideFollow', () => {
  const prog = deviceProgram(1, 5);

  it('activates a connected, unique, respondPC device program', () => {
    const d = decideFollow(prog, [prog], true, true, ALL_HYDRATED);
    expect(d).toEqual({ ok: true, midiProg: 5 });
  });

  it('blocks when follow is off', () => {
    const d = decideFollow(prog, [prog], true, false, ALL_HYDRATED);
    expect(d.ok).toBe(false);
    if (!d.ok) expect(d.reason).toMatch(/off/);
  });

  it('blocks when not connected', () => {
    const d = decideFollow(prog, [prog], false, true, ALL_HYDRATED);
    expect(d.ok).toBe(false);
    if (!d.ok) expect(d.reason).toMatch(/not connected/);
  });

  it('blocks when no program is selected', () => {
    const d = decideFollow(undefined, [prog], true, true, ALL_HYDRATED);
    expect(d.ok).toBe(false);
    if (!d.ok) expect(d.reason).toMatch(/no program/);
  });

  it('blocks local-only programs (PC would select something else)', () => {
    const local = deviceProgram(2, 7, { source: 'local' });
    const d = decideFollow(local, [local], true, true, ALL_HYDRATED);
    expect(d.ok).toBe(false);
    if (!d.ok) expect(d.reason).toMatch(/local-only/);
  });

  it('blocks programs with RESPOND TO PC disabled', () => {
    const deaf = deviceProgram(3, 9, { respondPC: false });
    const d = decideFollow(deaf, [deaf], true, true, ALL_HYDRATED);
    expect(d.ok).toBe(false);
    if (!d.ok) expect(d.reason).toMatch(/RESPOND TO PC/);
  });

  it('blocks when another respondPC device program shares the prog #', () => {
    const twin = deviceProgram(2, 5);
    const d = decideFollow(prog, [prog, twin], true, true, ALL_HYDRATED);
    expect(d.ok).toBe(false);
    if (!d.ok) expect(d.reason).toMatch(/share MIDI prog/);
  });

  it('ignores deaf sharers — a respondPC=false twin cannot respond, so no ambiguity', () => {
    const deafTwin = deviceProgram(2, 5, { respondPC: false });
    const d = decideFollow(prog, [prog, deafTwin], true, true, ALL_HYDRATED);
    expect(d).toEqual({ ok: true, midiProg: 5 });
  });

  it('ignores local sharers — they are not on the device at all', () => {
    const localTwin = deviceProgram(2, 5, { source: 'local' });
    const d = decideFollow(prog, [prog, localTwin], true, true, ALL_HYDRATED);
    expect(d).toEqual({ ok: true, midiProg: 5 });
  });

  it('blocks when the selected program is still a stub (header not read)', () => {
    const d = decideFollow(prog, [prog], true, true, NONE_HYDRATED);
    expect(d.ok).toBe(false);
    if (!d.ok) expect(d.reason).toMatch(/still reading this program/);
  });

  it('blocks when OTHER device headers are unread — stub defaults could hide a sharer', () => {
    // Regression for the real-hardware case: two programs both at
    // prog #0, but the unselected one still a stub claiming #1.
    // Trusting the stub would have called #0 unique and fired.
    const stubTwin = deviceProgram(2, 1);
    const hydratedOnlySelected = (slot: number) => slot === prog.slot;
    const d = decideFollow(prog, [prog, stubTwin], true, true, hydratedOnlySelected);
    expect(d.ok).toBe(false);
    if (!d.ok) expect(d.reason).toMatch(/still reading program headers/);
  });

  it('ignores hydration state of local programs — they have real local data', () => {
    const local = deviceProgram(2, 9, { source: 'local' });
    const hydratedOnlySelected = (slot: number) => slot === prog.slot;
    const d = decideFollow(prog, [prog, local], true, true, hydratedOnlySelected);
    expect(d).toEqual({ ok: true, midiProg: 5 });
  });
});

describe('activation lifecycle', () => {
  it('invalidateActivation clears only a matching slot', async () => {
    const m = await import('./followsync');
    m.lastActivation.set({ slot: 3, midiProg: 7, name: 'X', channel: 0 });
    m.invalidateActivation(5);
    expect(get(m.lastActivation)).not.toBeNull();
    m.invalidateActivation(3);
    expect(get(m.lastActivation)).toBeNull();
  });

  it('resetFollowState wipes both the claim and the reason', async () => {
    const m = await import('./followsync');
    m.lastActivation.set({ slot: 1, midiProg: 0, name: 'A', channel: 0 });
    m.followBlockedReason.set('whatever');
    m.resetFollowState();
    expect(get(m.lastActivation)).toBeNull();
    expect(get(m.followBlockedReason)).toBe('');
  });

  it('losing the connection drops the "PC sent" claim; reconnecting clears stale reasons', async () => {
    const m = await import('./followsync');
    const { phase } = await import('./connection');
    phase.set('connected');
    m.lastActivation.set({ slot: 1, midiProg: 0, name: 'A', channel: 0 });
    phase.set('unknown');
    expect(get(m.lastActivation)).toBeNull();
    m.followBlockedReason.set('not connected');
    phase.set('connected');
    expect(get(m.followBlockedReason)).toBe('');
  });

  it('a blocked re-selection displaces a matching earlier success', async () => {
    vi.useFakeTimers();
    try {
      const m = await import('./followsync');
      const { programs } = await import('./programs');
      const { phase } = await import('./connection');
      const prog = deviceProgram(4, 6, { respondPC: false });
      programs.set([prog]);
      phase.set('connected');
      m.followEnabled.set(true);
      // Pretend an earlier selection of slot 4 succeeded…
      m.lastActivation.set({ slot: 4, midiProg: 6, name: prog.name, channel: 0 });
      // …then the user re-selects it after turning RESPOND TO PC off.
      m.onProgramSelected(4);
      await vi.advanceTimersByTimeAsync(500);
      expect(get(m.lastActivation)).toBeNull();
      expect(get(m.followBlockedReason)).toMatch(/RESPOND TO PC/);
    } finally {
      vi.useRealTimers();
    }
  });

  it('end-to-end: a clean selection sends the PC and stamps lastActivation', async () => {
    vi.useFakeTimers();
    try {
      const m = await import('./followsync');
      const { programs } = await import('./programs');
      const { phase, status } = await import('./connection');
      const App = await import('../../../wailsjs/go/main/App');
      const prog = deviceProgram(2, 9);
      programs.set([prog]);
      phase.set('connected');
      status.set({ connected: true, channel: 3 } as any);
      m.followEnabled.set(true);
      m.onProgramSelected(2);
      await vi.advanceTimersByTimeAsync(500);
      expect((App as any).ActivateProgram).toHaveBeenCalledWith(9, 3);
      expect(get(m.lastActivation)).toEqual({ slot: 2, midiProg: 9, name: prog.name, channel: 3 });
      expect(get(m.followBlockedReason)).toBe('');
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('followEnabled persistence', () => {
  beforeEach(() => {
    localStorage.clear();
    vi.resetModules();
  });

  it('defaults to on when no preference is stored', async () => {
    const m = await import('./followsync');
    expect(get(m.followEnabled)).toBe(true);
  });

  it('honours a stored off preference and persists toggles', async () => {
    localStorage.setItem('s950-tools.followProgram', '0');
    const m = await import('./followsync');
    expect(get(m.followEnabled)).toBe(false);
    m.followEnabled.set(true);
    expect(localStorage.getItem('s950-tools.followProgram')).toBe('1');
  });
});
