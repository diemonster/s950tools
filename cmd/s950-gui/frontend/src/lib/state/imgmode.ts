// IMG editor mode — an explicit offline state for working on
// Gotek/FlashFloppy patch images without a sampler. The app's
// editing surface already works on local rows; this mode is the
// honest framing around it: connection affordances disappear (so a
// mid-edit Connect can't merge the device catalog into the image
// being edited), and open/save become Save / Save As against the
// image file instead of session import/export.
//
// Entered only while disconnected (the topbar button is hidden when
// connected, and enterImgMode double-checks). Not persisted — a
// fresh launch starts in normal mode.

import { writable, derived, get } from 'svelte/store';
import { phase } from './connection';

export const imgMode = writable<boolean>(false);

// imageFile is the absolute path of the most recently opened patch
// image — the "Save" target in editor mode. Kept across mode exits
// so re-entering remembers the file.
export const imageFile = writable<string | null>(null);

// imageSkipped lists files the open couldn't import (compressed
// samples, unknown types, unreadable entries — strings with
// reasons). Saving rebuilds the image from app state only, so these
// WILL be dropped on save; editor mode warns before letting that
// happen. Phase 2 (opaque carry-through) would empty this list.
export const imageSkipped = writable<string[]>([]);

// forceRS232 mirrors the export option: patch the image's OVERALL
// SE so the S950 boots with controller-select on RS-232C. Default
// on (this app's whole workflow runs over RS-232); persisted so
// people editing disks for stock samplers only have to opt out
// once.
const RS232_KEY = 's950-tools.imgForceRS232';
function readForceRS232(): boolean {
  if (typeof localStorage === 'undefined') return true;
  return localStorage.getItem(RS232_KEY) !== '0';
}
export const forceRS232 = writable<boolean>(readForceRS232());
forceRS232.subscribe((v) => {
  if (typeof localStorage !== 'undefined') {
    localStorage.setItem(RS232_KEY, v ? '1' : '0');
  }
});

// imageName is the display basename for the topbar chip.
export const imageName = derived(imageFile, (p) =>
  p ? p.split(/[\\/]/).pop() ?? p : '');

export function enterImgMode(): void {
  // Hidden in the UI while connected, but guard anyway — entering
  // the offline editor with a live device link would contradict
  // everything the mode promises.
  if (get(phase) === 'connected') return;
  imgMode.set(true);
}

export function exitImgMode(): void {
  imgMode.set(false);
}
