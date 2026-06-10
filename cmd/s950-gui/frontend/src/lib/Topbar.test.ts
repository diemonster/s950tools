// Component tests for the Topbar's System modal — specifically the
// host waveform-cache maintenance section added alongside the OVS
// form. The interesting wiring:
//   • the cache section renders even when GetOverall FAILS (host
//     action must not require a connected sampler)
//   • two-step confirm: Clear… → Confirm/Keep → ClearWaveformCache
//   • Keep backs out without calling the binding
//   • binding rejection surfaces as an inline error, not a crash

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { tick } from 'svelte';
import { render, fireEvent, cleanup } from '@testing-library/svelte';

// Hoisted mocks must precede component imports (same pattern as
// Sample.test.ts). Topbar's onMount enumerates ports + status, so
// those bindings need resolvable defaults.
vi.mock('../../wailsjs/go/main/App', () => ({
  ApplySlicing:       vi.fn().mockResolvedValue(undefined),
  Catalog:            vi.fn().mockResolvedValue({ programs: [], samples: [] }),
  ClearWaveformCache: vi.fn().mockResolvedValue(undefined),
  Connect:            vi.fn().mockResolvedValue(undefined),
  ConnectSerial:      vi.fn().mockResolvedValue(undefined),
  CopySampleAudio:    vi.fn().mockResolvedValue(null),
  Disconnect:         vi.fn().mockResolvedValue(undefined),
  GetCachedWaveform:  vi.fn().mockResolvedValue(null),
  GetOverall:         vi.fn().mockResolvedValue({ ProgName: 'TEST      ', BasicChannel: 0 }),
  GetProgram:         vi.fn().mockResolvedValue({}),
  GetSampleParams:    vi.fn().mockResolvedValue({}),
  ImportSample:       vi.fn().mockResolvedValue(null),
  InspectSlicing:     vi.fn().mockResolvedValue({ ok: true }),
  ListPorts:          vi.fn().mockResolvedValue({ ins: [], outs: [], serial: [] }),
  MidiThruCaps:       vi.fn().mockResolvedValue({ virtualSupported: true, virtualPortName: 'S950 RS-232 (s950-tools)', hint: '' }),
  MidiThruStart:      vi.fn().mockResolvedValue({ active: true, source: 'X', virtual: false, forwarded: 0, dropped: 0 }),
  MidiThruStatus:     vi.fn().mockResolvedValue({ active: false, source: '', virtual: false, forwarded: 0, dropped: 0 }),
  MidiThruStop:       vi.fn().mockResolvedValue({ active: false, source: '', virtual: false, forwarded: 0, dropped: 0 }),
  ProbeForS950:       vi.fn().mockResolvedValue({ port: '', baud: 38400 }),
  PutCachedWaveform:  vi.fn().mockResolvedValue(undefined),
  SendSample:         vi.fn().mockResolvedValue(undefined),
  SetOverall:         vi.fn().mockResolvedValue(undefined),
  SetProgram:         vi.fn().mockResolvedValue(undefined),
  SetSampleParams:    vi.fn().mockResolvedValue(undefined),
  Status:             vi.fn().mockResolvedValue({ connected: false, channel: 0 }),
  VerifyDevice:       vi.fn().mockResolvedValue(undefined),
}));

vi.mock('../../wailsjs/runtime/runtime', () => ({
  EventsOn:  vi.fn(() => () => {}),
  EventsOff: vi.fn(),
}));

import * as App from '../../wailsjs/go/main/App';
import Topbar from './Topbar.svelte';
import { phase } from './state/connection';

beforeEach(() => {
  vi.clearAllMocks();
  // The ⚙ System chip only renders while connected (the modal's OVS
  // form needs the device). The cache-clear scenarios below include
  // the "connected at transport level but sampler silent" case via
  // a GetOverall rejection — that's the RS-232 silent-failure mode.
  phase.set('connected');
  // Re-install defaults clobbered by clearAllMocks.
  ((App as any).GetOverall as ReturnType<typeof vi.fn>)
    .mockResolvedValue({ ProgName: 'TEST      ', BasicChannel: 0 });
  ((App as any).ListPorts as ReturnType<typeof vi.fn>)
    .mockResolvedValue({ ins: [], outs: [], serial: [] });
  ((App as any).Status as ReturnType<typeof vi.fn>)
    .mockResolvedValue({ connected: false, channel: 0 });
  ((App as any).ClearWaveformCache as ReturnType<typeof vi.fn>)
    .mockResolvedValue(undefined);
});

// Opens the System modal and waits for the GetOverall promise (or
// its rejection) to settle so the cache section is on screen.
async function openSystemModal(container: HTMLElement) {
  const sysBtn = Array.from(container.querySelectorAll('button'))
    .find((b) => /system/i.test(b.textContent ?? '')) as HTMLButtonElement;
  expect(sysBtn).toBeTruthy();
  await fireEvent.click(sysBtn);
  await tick(); // open + loading
  await tick(); // GetOverall settles
  await tick(); // re-render
}

function findButton(container: HTMLElement, re: RegExp): HTMLButtonElement | undefined {
  return Array.from(container.querySelectorAll('button'))
    .find((b) => re.test(b.textContent ?? '')) as HTMLButtonElement | undefined;
}

describe('System modal — host waveform cache section', () => {
  it('walks the two-step confirm and calls ClearWaveformCache', async () => {
    const { container } = render(Topbar);
    await openSystemModal(container);

    // Step 1: the idle CTA.
    const clearBtn = findButton(container, /clear waveform cache/i);
    expect(clearBtn).toBeTruthy();
    await fireEvent.click(clearBtn!);
    await tick();

    // Step 2: confirm + keep are both offered; nothing called yet.
    expect((App as any).ClearWaveformCache).not.toHaveBeenCalled();
    const confirmBtn = findButton(container, /confirm clear/i);
    expect(confirmBtn).toBeTruthy();

    await fireEvent.click(confirmBtn!);
    await tick();
    await tick();

    expect((App as any).ClearWaveformCache).toHaveBeenCalledTimes(1);
    expect(container.textContent).toMatch(/cache cleared/i);
    cleanup();
  });

  it('Keep backs out without clearing', async () => {
    const { container } = render(Topbar);
    await openSystemModal(container);

    await fireEvent.click(findButton(container, /clear waveform cache/i)!);
    await tick();
    await fireEvent.click(findButton(container, /keep/i)!);
    await tick();

    expect((App as any).ClearWaveformCache).not.toHaveBeenCalled();
    // Back to the idle CTA.
    expect(findButton(container, /clear waveform cache/i)).toBeTruthy();
    cleanup();
  });

  it('stays reachable when GetOverall fails (sampler disconnected)', async () => {
    ((App as any).GetOverall as ReturnType<typeof vi.fn>)
      .mockRejectedValueOnce(new Error('no reply within 2s'));
    const { container } = render(Topbar);
    await openSystemModal(container);

    // OVS error surfaced…
    expect(container.textContent).toMatch(/no reply within 2s/);
    // …but the host-side cache action is still available.
    expect(findButton(container, /clear waveform cache/i)).toBeTruthy();
    cleanup();
  });

  it('surfaces a binding failure as inline error', async () => {
    ((App as any).ClearWaveformCache as ReturnType<typeof vi.fn>)
      .mockRejectedValueOnce(new Error('permission denied'));
    const { container } = render(Topbar);
    await openSystemModal(container);

    await fireEvent.click(findButton(container, /clear waveform cache/i)!);
    await tick();
    await fireEvent.click(findButton(container, /confirm clear/i)!);
    await tick();
    await tick();

    expect(container.textContent).toMatch(/permission denied/);
    cleanup();
  });

  it('re-opening the modal resets a stale done/confirm state', async () => {
    const { container } = render(Topbar);
    await openSystemModal(container);

    // Clear once → done note shown.
    await fireEvent.click(findButton(container, /clear waveform cache/i)!);
    await tick();
    await fireEvent.click(findButton(container, /confirm clear/i)!);
    await tick();
    await tick();
    expect(container.textContent).toMatch(/cache cleared/i);

    // Close + re-open → back to the idle CTA, not the done note.
    await fireEvent.click(findButton(container, /close|cancel/i)!);
    await tick();
    await openSystemModal(container);
    expect(findButton(container, /clear waveform cache/i)).toBeTruthy();
    expect(container.textContent).not.toMatch(/cache cleared/i);
    cleanup();
  });
});
