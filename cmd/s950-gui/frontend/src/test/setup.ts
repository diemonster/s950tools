// Vitest global setup. Runs once per test worker before any test
// file. Stubs the Wails bindings + browser APIs that jsdom doesn't
// implement (URL.createObjectURL is not strictly needed today, but
// AudioContext + getBoundingClientRect routinely show up in any
// component test that touches the waveform).
//
// Module mocks need to be installed via vi.mock() inside each test
// file because vi.mock is hoisted per-file. This setup handles the
// global polyfills only.

import { vi } from 'vitest';

// jsdom doesn't provide AudioContext; the preview module imports
// it lazily, but if any component test transitively constructs one
// we want a no-op stub rather than ReferenceError.
class StubAudioContext {
  state = 'suspended';
  currentTime = 0;
  destination = {};
  resume() { this.state = 'running'; return Promise.resolve(); }
  createBuffer(_ch: number, length: number, _rate: number) {
    const data = new Float32Array(length);
    return {
      length, sampleRate: _rate, numberOfChannels: _ch,
      getChannelData: () => data,
    };
  }
  createBufferSource() {
    return {
      buffer: null,
      loop: false,
      loopStart: 0,
      loopEnd: 0,
      onended: null,
      connect() {},
      disconnect() {},
      start() {},
      stop() {},
      addEventListener() {},
    };
  }
  // Minimal GainNode stub. `gain` is an AudioParam — only the
  // automation methods preview.ts actually calls need to be present.
  createGain() {
    const param = {
      value: 1,
      setValueAtTime(_v: number, _when: number) {},
      linearRampToValueAtTime(_v: number, _when: number) {},
    };
    return {
      gain: param,
      connect() {},
      disconnect() {},
    };
  }
}
// @ts-expect-error -- jsdom's global doesn't include AudioContext.
globalThis.AudioContext = StubAudioContext;
// @ts-expect-error -- same: no webkitAudioContext in the jsdom global.
globalThis.webkitAudioContext = StubAudioContext;

// jsdom returns zeroed rects for every element. Patch the prototype
// so component tests that depend on element width (waveform, slice
// markers) get a sane positive number. Tests that need a specific
// rect can override the per-element method.
Element.prototype.getBoundingClientRect = function () {
  return {
    x: 0, y: 0, width: 800, height: 200,
    top: 0, left: 0, right: 800, bottom: 200,
    toJSON() { return this; },
  } as DOMRect;
};

// Suppress noisy console output from expected error paths so test
// output stays focused on assertion failures. Restore explicitly
// inside any test that asserts log content.
vi.spyOn(console, 'warn').mockImplementation(() => {});
vi.spyOn(console, 'info').mockImplementation(() => {});
