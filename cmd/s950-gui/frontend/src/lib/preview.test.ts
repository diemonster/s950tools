import { describe, it, expect } from 'vitest';
import { hasHostAudio } from './preview';
import { newLocalSample } from './state/samples';

// Most of preview.ts is Web Audio-bound and isn't worth mocking
// AudioContext/AudioBufferSource for — those paths are exercised
// manually in the dev app. hasHostAudio is the one pure predicate
// the UI consumes for button-disabled state, so we lock its
// contract here.

describe('hasHostAudio', () => {
  it('returns false for undefined', () => {
    expect(hasHostAudio(undefined)).toBe(false);
  });
  it('returns false when pcm is missing', () => {
    expect(hasHostAudio(newLocalSample(0, 'X', 26040, 100))).toBe(false);
  });
  it('returns false when pcm is empty', () => {
    const s = { ...newLocalSample(0, 'X', 26040, 100), pcm: [] };
    expect(hasHostAudio(s)).toBe(false);
  });
  it('returns true when pcm has content', () => {
    const s = { ...newLocalSample(0, 'X', 26040, 100), pcm: [0, 1, 2] };
    expect(hasHostAudio(s)).toBe(true);
  });
});
