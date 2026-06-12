// IMG editor mode state tests: the connected-guard on entry (the
// mode's whole promise is "no device link"), the forceRS232
// persistence round-trip, and the filename derivation the topbar
// chip renders.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';

vi.mock('../../../wailsjs/go/main/App', () => ({
  Connect:       vi.fn().mockResolvedValue(undefined),
  ConnectSerial: vi.fn().mockResolvedValue(undefined),
  Disconnect:    vi.fn().mockResolvedValue(undefined),
  ListPorts:     vi.fn().mockResolvedValue({ ins: [], outs: [], serial: [] }),
  Status:        vi.fn().mockResolvedValue({ connected: false, channel: 0 }),
  VerifyDevice:  vi.fn().mockResolvedValue(undefined),
}));
vi.mock('./catalog', () => ({ refreshCatalog: vi.fn() }));

beforeEach(() => {
  localStorage.clear();
  vi.resetModules();
});

describe('imgMode entry/exit', () => {
  it('enters only while disconnected', async () => {
    const m = await import('./imgmode');
    const { phase } = await import('./connection');
    phase.set('connected');
    m.enterImgMode();
    expect(get(m.imgMode)).toBe(false);
    phase.set('unknown');
    m.enterImgMode();
    expect(get(m.imgMode)).toBe(true);
    m.exitImgMode();
    expect(get(m.imgMode)).toBe(false);
  });

  it('keeps the image file across mode exits', async () => {
    const m = await import('./imgmode');
    m.imageFile.set('/disks/kit.img');
    m.enterImgMode();
    m.exitImgMode();
    expect(get(m.imageFile)).toBe('/disks/kit.img');
  });
});

describe('imageName', () => {
  it('derives the basename for unix and windows paths', async () => {
    const m = await import('./imgmode');
    m.imageFile.set('/Users/me/disks/S9x JV Techno.img');
    expect(get(m.imageName)).toBe('S9x JV Techno.img');
    m.imageFile.set('C:\\disks\\kit.img');
    expect(get(m.imageName)).toBe('kit.img');
    m.imageFile.set(null);
    expect(get(m.imageName)).toBe('');
  });
});

describe('forceRS232 persistence', () => {
  it('defaults on, persists an opt-out', async () => {
    const m = await import('./imgmode');
    expect(get(m.forceRS232)).toBe(true);
    m.forceRS232.set(false);
    expect(localStorage.getItem('s950-tools.imgForceRS232')).toBe('0');
  });

  it('honours a stored opt-out on next load', async () => {
    localStorage.setItem('s950-tools.imgForceRS232', '0');
    const m = await import('./imgmode');
    expect(get(m.forceRS232)).toBe(false);
  });
});
