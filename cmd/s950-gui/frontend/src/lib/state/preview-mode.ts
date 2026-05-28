// Preview mode toggle — controls where the Sample-tab play buttons
// route audio. Two values:
//   'host'   — Web Audio playback of host-side PCM (instant, no wire
//              traffic, works for any imported / cached sample).
//   'device' — MIDI Note On to the S950 so the device plays the
//              sample through its own outputs. Only triggers
//              audibly when a program currently maps the chosen
//              note to the sample; otherwise silence.
//
// Stored globally rather than per-sample: the user typically wants
// the same routing for the whole session, and per-sample state
// makes the dropdown's behavior surprising ("why did changing
// samples flip my mode?"). localStorage persistence so the choice
// survives reloads.

import { writable } from 'svelte/store';

export type PreviewMode = 'host' | 'device';

const KEY = 's950-tools.preview-mode';

function read(): PreviewMode {
  if (typeof localStorage === 'undefined') return 'host';
  return localStorage.getItem(KEY) === 'device' ? 'device' : 'host';
}
function write(v: PreviewMode) {
  if (typeof localStorage === 'undefined') return;
  localStorage.setItem(KEY, v);
}

export const previewMode = writable<PreviewMode>(read());
previewMode.subscribe(write);

// PREVIEW_NOTE is the MIDI note we send when in device mode. C3
// (60) is the convention most kits use for the sample's "natural"
// trigger; the user is expected to have a program mapped to this
// note. Future enhancement: derive from the sample's
// nominal-pitch SPRM field for more accurate per-sample triggers.
export const PREVIEW_NOTE = 60;

// PREVIEW_VELOCITY is the Note On velocity. 127 (max) bypasses any
// velocity-crossfade splits and triggers the loudest mapped layer
// — most reliable for a preview that's meant to be audible.
export const PREVIEW_VELOCITY = 127;
