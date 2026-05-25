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
  Connect:    vi.fn().mockResolvedValue(undefined),
  Disconnect: vi.fn().mockResolvedValue(undefined),
  ListPorts:  vi.fn().mockResolvedValue({ ins: [], outs: [] }),
  Status:     vi.fn().mockResolvedValue({ connected: false, channel: 0 }),
}));
// Avoid the cascading catalog import — refreshCatalog isn't called
// in any of these tests, so a stub keeps the dep graph small.
vi.mock('./catalog', () => ({ refreshCatalog: vi.fn() }));

const KEY_IN      = 's950-tools.midi-in';
const KEY_OUT     = 's950-tools.midi-out';
const KEY_CHANNEL = 's950-tools.midi-channel';

beforeEach(() => {
  localStorage.clear();
  vi.resetModules();
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
    selectedIn.set('MRCC Port 03');
    selectedIn.set('');
    expect(localStorage.getItem(KEY_IN)).toBe('');
  });
});
