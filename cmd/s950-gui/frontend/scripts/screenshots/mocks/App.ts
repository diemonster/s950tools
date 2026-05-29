// MOCK wailsjs/go/main/App — only loaded when MOCK_WAILS=1 (vite alias).
// Returns realistic-density payloads so headless-Chrome screenshots
// surface real-content overflow (long names, full keygroup tables,
// drawn waveforms, populated sample sidebar).

// Names with deliberately wide variation, including a few long ones
// to stress Combobox / table cells / chip widths.
const SAMPLE_NAMES = [
  'KICK 909', 'KICK 808', 'SNARE_MAPEX_LAYER', 'CL.HAT', 'OP.HAT',
  'CRASH RIDE 17', 'TOM HI', 'TOM MID', 'TOM LO', 'CLAP_LAYER_X',
  'RIM SHOT', 'COWBELL_MUTED', 'CONGA HI', 'CONGA LO', 'BONGO',
  'SHAKER', 'TAMBOURINE', 'TRIANGLE STRIKE', 'WOODBLOCK_HARD',
  'BASS C2 PLUCK', 'BASS_SUB_LONG_NAME_TEST', 'BASS A1',
  'STRINGS C3', 'STRINGS E3', 'STRINGS G3', 'PAD WARM',
  'PAD_EVOLVING_LONG', 'LEAD SAW', 'LEAD SQUARE', 'PIANO C4',
  'PIANO E4', 'RHODES F3', 'WURLI D4', 'ORGAN A3',
  'CHOIR AAH', 'VOX OOH', 'VOX MMH', 'GLITCH_FX_01',
  'NOISE WHITE', 'NOISE PINK', 'IMPACT SLAM', 'RISER UP',
  'DOWNER', 'TEXTURE_GRANULAR', 'FOLEY DOOR', 'FOLEY GLASS',
  'GUITAR_PALM_MUTE_CHUG', 'BRASS HIT', 'FLUTE C5', 'CELLO C2',
];
const PROGRAM_NAMES = [
  'DRUM KIT 1', 'DRUM KIT 2', 'DRUM KIT 909', 'PERC LATIN',
  'TR808_FULL_KIT_LAYERED', 'BASS PROG', 'STRINGS', 'PIANO',
  'RHODES', 'WURLI', 'ORGAN', 'PAD WARM',
  'PAD_EVOLVING_DARK', 'LEAD SAW', 'LEAD SQUARE', 'CHOIR',
  'BRASS', 'FLUTE', 'CELLO', 'GLITCH KIT',
  'FOLEY KIT', 'SFX RISERS', 'SFX IMPACTS', 'IMS_DRUM',
  'AMBIENT_TEXTURES', 'BASS SUB', 'BASS PLUCK', 'CHORUS C',
  'CHORUS G', 'EXPERIMENTAL',
];

const ZONE_COLORS = [
  '--rb-yellow', '--rb-magenta', '--rb-cyan', '--rb-green',
  '--rb-orange', '--rb-red', '--rb-purple', '--rb-blue', '--rb-pink',
];

// ---------- Catalog ----------
export async function Catalog() {
  return {
    programs: PROGRAM_NAMES.map((name, i) => ({ kind: 'program', slot: i, name })),
    samples:  SAMPLE_NAMES.map((name, i) => ({ kind: 'sample',  slot: i, name })),
  };
}

// ---------- Program (dense kit with 24 keygroups across the keyboard) ----------
function makeKeygroup(i: number) {
  // Spread 24 keygroups across the 88-key range (21..108).
  const total = 24;
  const span = 88 / total;
  const lo = Math.floor(21 + i * span);
  const hi = Math.min(108, Math.floor(21 + (i + 1) * span) - 1);
  // Pick two distinct sample names so layer columns show real content.
  const softIdx = (i * 3) % SAMPLE_NAMES.length;
  const loudIdx = (i * 3 + 7) % SAMPLE_NAMES.length;
  return {
    lower_key: lo,
    upper_key: hi,
    velocity_switch: i % 3 === 0 ? 80 : 128,
    attack: 5, decay: 80, sustain: 99, release: 30,
    filter_attack: 20, filter_decay: 20, filter_sustain: 20, filter_release: 20,
    filter_vel_int: 10,
    filter_key_tracking: 50,
    attack_vel_int: 0,
    vel_release_int: 0,
    loudness_vel_int: 30,
    pitch_warp_vel_int: 0,
    pitch_warp_offset: 0,
    pitch_warp_recovery: 50,
    adsr_to_vcf: 0,
    aftertouch_depth_mod: 0,
    mod_wheel_lfo_depth_mod: 50,
    lfo_build_time: 64,
    lfo_rate: 42,
    lfo_depth: 0,
    control_bits: 4,
    voice_out_assign: 255,
    midi_offset: 16,
    vel_xfade_50pct: 0,
    soft_sample:  SAMPLE_NAMES[softIdx],
    soft_tune: 0, soft_filter: 99, soft_loudness: 0,
    loud_sample:  SAMPLE_NAMES[loudIdx],
    loud_tune: 0, loud_filter: 99, loud_loudness: 0,
    _raw_bytes_hex: '',
  };
}

const PROGRAM_FIXTURE = {
  name: PROGRAM_NAMES[0],
  key_tilt: 0,
  positional_xfade: false,
  num_keygroups: 24,
  midi_program_number: 1,
  enable_midi_program: true,
  samples: [],
  keygroups: Array.from({ length: 24 }, (_, i) => makeKeygroup(i)),
  _raw_header_hex: '',
};

export async function GetProgram(_slot: number) {
  return PROGRAM_FIXTURE;
}

export async function SetProgram(_slot: number, _program: any) {}

// ---------- Sample ----------
export async function GetSampleParams(slot: number) {
  const name = SAMPLE_NAMES[slot % SAMPLE_NAMES.length];
  // Pick a length that's big enough to actually draw a waveform
  // (~120k 12-bit words at 26 kHz ≈ 4.6 sec).
  const len = 120_000;
  return {
    Raw: [],
    Name: name,
    TotalWords: len,
    SampleRateHz: 26_040,
    NominalPitch: 60,
    LoudOffset: 0,
    ReplayMode: 0,
    End: len,
    Start: 0,
    LoopLength: 0,
    VelXFade: 0,
    Reversed: 0,
  };
}

export async function SetSampleParams(_slot: number, _params: any) {}

// ---------- Audio buffers ----------
// Generate a 12-bit offset-binary waveform — sine × decaying envelope.
// The waveform renderer is the densest visual element, so we want it
// to actually render shapes (silence would hide overflow in the
// canvas region).
function makeWaveform(n = 120_000): number[] {
  const out = new Array<number>(n);
  for (let i = 0; i < n; i++) {
    const t = i / n;
    const env = Math.exp(-t * 1.4);
    const freq = 440 + 80 * Math.sin(t * 6);
    const s = Math.sin(2 * Math.PI * freq * (i / 26040)) * env;
    out[i] = Math.round(2048 + s * 1900);
  }
  return out;
}
const WAVEFORM_FIXTURE = makeWaveform();

export async function CopySampleAudio(_slot: number) {
  return WAVEFORM_FIXTURE;
}
export async function GetCachedWaveform(_slot: number, _name: string, _len: number) {
  return WAVEFORM_FIXTURE;
}
export async function PutCachedWaveform(_a: number, _b: string, _c: number, _d: number[]) {}
export async function ClearWaveformCache() {}

// ---------- Connection ----------
export async function ListPorts() {
  return {
    ins: [
      { name: 'IAC Driver Bus 1' },
      { name: 'MRCC 880 Port 1' },
      { name: 'MRCC 880 Port 2 — Sequencer Out (Studio)' },
    ],
    outs: [
      { name: 'IAC Driver Bus 1' },
      { name: 'MRCC 880 Port 1' },
      { name: 'MRCC 880 Port 2 — Sequencer Out (Studio)' },
    ],
    serial: [
      { name: '/dev/tty.usbserial-AB0123' },
      { name: '/dev/tty.usbserial-FT1234-with-a-deliberately-long-trailing-name' },
    ],
  };
}
export async function Connect(_in: string, _out: string, _ch: number) {}
export async function ConnectSerial(_port: string, _baud: number) {}
export async function Disconnect() {}
export async function Status() {
  // Connected by default so all the live UI states render and the
  // Topbar shows the realistic post-connect chip set.
  return {
    connected: true,
    kind: 'serial',
    in: '/dev/tty.usbserial-AB0123',
    out: '/dev/tty.usbserial-AB0123',
    channel: 0,
    baud: 38400,
  };
}
export async function ProbeForS950() {
  return { port: '/dev/tty.usbserial-AB0123', baud: 38400 };
}

// ---------- File ops (no-op stubs) ----------
export async function SaveProgramJSON(_p: any, _path: string) { return _path || '/tmp/program.json'; }
export async function OpenProgramJSON() { return PROGRAM_FIXTURE; }
export async function SaveSampleWav(_path: string, _rate: number, _words: number[]) { return _path || '/tmp/sample.wav'; }
export async function ImportSample(_path: string) {
  return {
    path: _path || '/tmp/import.wav',
    name: 'IMPORTED',
    rate: 44_100,
    length: 120_000,
    pcm: [],
    words: WAVEFORM_FIXTURE,
  };
}
export async function ResampleSample(_words: number[], _from: number, _to: number) {
  return { pcm: [], words: WAVEFORM_FIXTURE, rate: _to, length: WAVEFORM_FIXTURE.length };
}
export async function SendSample(_slot: number, _words: number[], _rate: number, _params: any) {}
export async function InspectSlicing(_req: any) {
  return {
    ok: true,
    errors: [],
    warnings: ['Sample slot 12 will be overwritten', 'Program slot 3 will be overwritten'],
    sampleSlots: [12, 13, 14, 15],
    programSlot: 3,
    estimatedSeconds: 12,
    occupiedSlots: [],
  };
}
export async function ApplySlicing(_req: any) {}
export async function PreviewMidi(_ch: number, _key: number, _vel: number, _ms: number) {}

// ---------- Drum / Overall (unused by frontend but present in type) ----------
export async function GetDrum() { return { Bytes: [] }; }
export async function SetDrum(_d: any) {}
export async function GetOverall() {
  return {
    ProgName: PROGRAM_NAMES[0],
    MidiTxChannel: 0,
    RxSimChannel: 0,
    RxSimKey: 60,
    RxSimVelocity: 100,
    BasicChannel: 0,
    OmniOn: false,
    LoudnessOnCC7: false,
    ControllerSelect: 0,
    MPEEnabled: false,
    PitchWheelRange: 2,
    BaudRate: 38400,
    Raw: [],
  };
}
export async function SetOverall(_o: any) {}
