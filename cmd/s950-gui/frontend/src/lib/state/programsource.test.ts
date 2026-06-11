// Tests for the Program.source origin marker — the guard rails that
// keep local programs (New Program, Open .json, Open Gotek .img) and
// device slots from clobbering each other in either direction.
//
// The bug locked out here: importing a Gotek image while connected,
// then clicking each imported program, silently replaced every one
// with the device's "TONE PRGRM" placeholder (the lazy loader
// fetched device state over the local rows).

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';

vi.mock('../../../wailsjs/go/main/App', () => ({
  Catalog:         vi.fn().mockResolvedValue({ programs: [], samples: [] }),
  GetProgram:      vi.fn().mockResolvedValue(null),
  GetSampleParams: vi.fn().mockResolvedValue(null),
  SetProgram:      vi.fn().mockResolvedValue(undefined),
  SetSampleParams: vi.fn().mockResolvedValue(undefined),
  ListPorts:       vi.fn().mockResolvedValue({ ins: [], outs: [], serial: [] }),
  Status:          vi.fn().mockResolvedValue({ connected: false, channel: 0 }),
}));

import * as App from '../../../wailsjs/go/main/App';
import {
  ensureProgramLoaded, refreshCatalog, markProgramOnDevice, programJSONToProgram,
} from './catalog';
import { programs, newLocalProgram } from './programs';
import { samples } from './samples';
import { scheduleProgramWriteback } from './livesync';
import { phase } from './connection';

beforeEach(() => {
  vi.clearAllMocks();
  programs.set([]);
  samples.set([]);
  phase.set('connected');
  ((App as any).GetProgram as ReturnType<typeof vi.fn>).mockResolvedValue(null);
  ((App as any).Catalog as ReturnType<typeof vi.fn>).mockResolvedValue({ programs: [], samples: [] });
});

describe('ensureProgramLoaded — local-row guard', () => {
  it('never fetches device state over a local program (the TONE PRGRM clobber)', async () => {
    programs.set([{ ...newLocalProgram(3, 'IMPORTED'), source: 'local' as const }]);
    await ensureProgramLoaded(3);
    expect((App as any).GetProgram).not.toHaveBeenCalled();
    expect(get(programs)[0].name).toBe('IMPORTED');
  });

  it('force=true (explicit Get from S950) is the deliberate override', async () => {
    programs.set([{ ...newLocalProgram(3, 'IMPORTED'), source: 'local' as const }]);
    ((App as any).GetProgram as ReturnType<typeof vi.fn>).mockResolvedValue({
      name: 'DEVICEPROG', num_keygroups: 1, keygroups: [],
    });
    await ensureProgramLoaded(3, true);
    expect((App as any).GetProgram).toHaveBeenCalledWith(3);
    const p = get(programs)[0];
    expect(p.name).toBe('DEVICEPROG');
    expect(p.source).toBe('device');
  });

  it('skips slots with no row at all', async () => {
    await ensureProgramLoaded(42);
    expect((App as any).GetProgram).not.toHaveBeenCalled();
  });
});

describe('refreshCatalog — local program preservation', () => {
  it('keeps local programs through a refresh; device wins slot collisions', async () => {
    programs.set([
      { ...newLocalProgram(5, 'KEEPME'), source: 'local' as const },
      { ...newLocalProgram(7, 'SHADOWED'), source: 'local' as const },
    ]);
    ((App as any).Catalog as ReturnType<typeof vi.fn>).mockResolvedValue({
      programs: [{ slot: 7, name: 'DEVICE7' }],
      samples: [],
    });
    await refreshCatalog();
    const list = get(programs);
    const names = list.map((p) => `${p.slot}:${p.name}`);
    expect(names).toContain('5:KEEPME');   // local survived
    expect(names).toContain('7:DEVICE7');  // device won the collision
    expect(names).not.toContain('7:SHADOWED');
  });
});

describe('scheduleProgramWriteback — source gate', () => {
  it('refuses to auto-write a local program to the device', async () => {
    programs.set([{ ...newLocalProgram(4, 'LOCALKIT'), source: 'local' as const }]);
    scheduleProgramWriteback(4);
    await new Promise((r) => setTimeout(r, 450)); // past the debounce
    expect((App as any).SetProgram).not.toHaveBeenCalled();
  });

  it('still auto-writes device programs', async () => {
    programs.set([{ ...newLocalProgram(4, 'DEVKIT'), source: 'device' as const }]);
    scheduleProgramWriteback(4);
    await new Promise((r) => setTimeout(r, 450));
    expect((App as any).SetProgram).toHaveBeenCalledTimes(1);
  });
});

describe('markProgramOnDevice — Send promotion', () => {
  it('flips a local program to device after explicit Send', () => {
    programs.set([{ ...newLocalProgram(9, 'SENTKIT'), source: 'local' as const }]);
    markProgramOnDevice(9);
    expect(get(programs)[0].source).toBe('device');
  });
});

describe('programJSONToProgram — source parameter', () => {
  it('defaults to device (the fetch path) and accepts local for imports', () => {
    const j = { name: 'X', keygroups: [] };
    expect(programJSONToProgram(j, 0).source).toBe('device');
    expect(programJSONToProgram(j, 0, 'local').source).toBe('local');
  });
});
