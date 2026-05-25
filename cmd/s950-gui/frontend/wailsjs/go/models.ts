export namespace device {
	
	export class SliceSpec {
	    name: string;
	    startWord: number;
	    lengthWords: number;
	    loopMode: string;
	    loopStart: number;
	    loopLength: number;
	
	    static createFrom(source: any = {}) {
	        return new SliceSpec(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.startWord = source["startWord"];
	        this.lengthWords = source["lengthWords"];
	        this.loopMode = source["loopMode"];
	        this.loopStart = source["loopStart"];
	        this.loopLength = source["loopLength"];
	    }
	}

}

export namespace main {
	
	export class CatalogItem {
	    kind: string;
	    slot: number;
	    name: string;
	
	    static createFrom(source: any = {}) {
	        return new CatalogItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.slot = source["slot"];
	        this.name = source["name"];
	    }
	}
	export class Catalog {
	    programs: CatalogItem[];
	    samples: CatalogItem[];
	
	    static createFrom(source: any = {}) {
	        return new Catalog(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.programs = this.convertValues(source["programs"], CatalogItem);
	        this.samples = this.convertValues(source["samples"], CatalogItem);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class ConnectionStatus {
	    connected: boolean;
	    in?: string;
	    out?: string;
	    channel: number;
	
	    static createFrom(source: any = {}) {
	        return new ConnectionStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.connected = source["connected"];
	        this.in = source["in"];
	        this.out = source["out"];
	        this.channel = source["channel"];
	    }
	}
	export class OccupiedSlot {
	    kind: string;
	    slot: number;
	    name: string;
	
	    static createFrom(source: any = {}) {
	        return new OccupiedSlot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.slot = source["slot"];
	        this.name = source["name"];
	    }
	}
	export class Port {
	    name: string;
	
	    static createFrom(source: any = {}) {
	        return new Port(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	    }
	}
	export class PortList {
	    ins: Port[];
	    outs: Port[];
	
	    static createFrom(source: any = {}) {
	        return new PortList(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ins = this.convertValues(source["ins"], Port);
	        this.outs = this.convertValues(source["outs"], Port);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SlicingPreflight {
	    ok: boolean;
	    errors?: string[];
	    warnings?: string[];
	    sampleSlots: number[];
	    programSlot: number;
	    estimatedSeconds: number;
	    occupiedSlots?: OccupiedSlot[];
	
	    static createFrom(source: any = {}) {
	        return new SlicingPreflight(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.errors = source["errors"];
	        this.warnings = source["warnings"];
	        this.sampleSlots = source["sampleSlots"];
	        this.programSlot = source["programSlot"];
	        this.estimatedSeconds = source["estimatedSeconds"];
	        this.occupiedSlots = this.convertValues(source["occupiedSlots"], OccupiedSlot);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SlicingRequest {
	    sourceWords: number[];
	    sourceRateHz: number;
	    slices: device.SliceSpec[];
	    baseName: string;
	    programName: string;
	    baseMidiKey: number;
	    firstSampleSlot: number;
	    programSlot: number;
	
	    static createFrom(source: any = {}) {
	        return new SlicingRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sourceWords = source["sourceWords"];
	        this.sourceRateHz = source["sourceRateHz"];
	        this.slices = this.convertValues(source["slices"], device.SliceSpec);
	        this.baseName = source["baseName"];
	        this.programName = source["programName"];
	        this.baseMidiKey = source["baseMidiKey"];
	        this.firstSampleSlot = source["firstSampleSlot"];
	        this.programSlot = source["programSlot"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace protocol {
	
	export class KeygroupJSON {
	    lower_key: number;
	    upper_key: number;
	    velocity_switch: number;
	    attack: number;
	    decay: number;
	    sustain: number;
	    release: number;
	    filter_attack: number;
	    filter_decay: number;
	    filter_sustain: number;
	    filter_release: number;
	    filter_vel_int: number;
	    filter_key_tracking: number;
	    attack_vel_int: number;
	    vel_release_int: number;
	    loudness_vel_int: number;
	    pitch_warp_vel_int: number;
	    pitch_warp_offset: number;
	    pitch_warp_recovery: number;
	    adsr_to_vcf: number;
	    aftertouch_depth_mod: number;
	    mod_wheel_lfo_depth_mod: number;
	    lfo_build_time: number;
	    lfo_rate: number;
	    lfo_depth: number;
	    control_bits: number;
	    voice_out_assign: number;
	    midi_offset: number;
	    vel_xfade_50pct: number;
	    soft_sample: string;
	    soft_tune: number;
	    soft_filter: number;
	    soft_loudness: number;
	    loud_sample: string;
	    loud_tune: number;
	    loud_filter: number;
	    loud_loudness: number;
	    _raw_bytes_hex: string;
	
	    static createFrom(source: any = {}) {
	        return new KeygroupJSON(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.lower_key = source["lower_key"];
	        this.upper_key = source["upper_key"];
	        this.velocity_switch = source["velocity_switch"];
	        this.attack = source["attack"];
	        this.decay = source["decay"];
	        this.sustain = source["sustain"];
	        this.release = source["release"];
	        this.filter_attack = source["filter_attack"];
	        this.filter_decay = source["filter_decay"];
	        this.filter_sustain = source["filter_sustain"];
	        this.filter_release = source["filter_release"];
	        this.filter_vel_int = source["filter_vel_int"];
	        this.filter_key_tracking = source["filter_key_tracking"];
	        this.attack_vel_int = source["attack_vel_int"];
	        this.vel_release_int = source["vel_release_int"];
	        this.loudness_vel_int = source["loudness_vel_int"];
	        this.pitch_warp_vel_int = source["pitch_warp_vel_int"];
	        this.pitch_warp_offset = source["pitch_warp_offset"];
	        this.pitch_warp_recovery = source["pitch_warp_recovery"];
	        this.adsr_to_vcf = source["adsr_to_vcf"];
	        this.aftertouch_depth_mod = source["aftertouch_depth_mod"];
	        this.mod_wheel_lfo_depth_mod = source["mod_wheel_lfo_depth_mod"];
	        this.lfo_build_time = source["lfo_build_time"];
	        this.lfo_rate = source["lfo_rate"];
	        this.lfo_depth = source["lfo_depth"];
	        this.control_bits = source["control_bits"];
	        this.voice_out_assign = source["voice_out_assign"];
	        this.midi_offset = source["midi_offset"];
	        this.vel_xfade_50pct = source["vel_xfade_50pct"];
	        this.soft_sample = source["soft_sample"];
	        this.soft_tune = source["soft_tune"];
	        this.soft_filter = source["soft_filter"];
	        this.soft_loudness = source["soft_loudness"];
	        this.loud_sample = source["loud_sample"];
	        this.loud_tune = source["loud_tune"];
	        this.loud_filter = source["loud_filter"];
	        this.loud_loudness = source["loud_loudness"];
	        this._raw_bytes_hex = source["_raw_bytes_hex"];
	    }
	}
	export class SampleSpec {
	    file: string;
	    name?: string;
	    rate?: string;
	    channel_mode?: string;
	    tune?: number;
	    max_frames?: number;
	    loop_start?: number;
	    loop_end?: number;
	    mode?: number;
	
	    static createFrom(source: any = {}) {
	        return new SampleSpec(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.file = source["file"];
	        this.name = source["name"];
	        this.rate = source["rate"];
	        this.channel_mode = source["channel_mode"];
	        this.tune = source["tune"];
	        this.max_frames = source["max_frames"];
	        this.loop_start = source["loop_start"];
	        this.loop_end = source["loop_end"];
	        this.mode = source["mode"];
	    }
	}
	export class ProgramJSON {
	    name: string;
	    key_tilt: number;
	    positional_xfade: boolean;
	    num_keygroups: number;
	    midi_program_number: number;
	    enable_midi_program: boolean;
	    samples?: SampleSpec[];
	    keygroups: KeygroupJSON[];
	    _raw_header_hex: string;
	
	    static createFrom(source: any = {}) {
	        return new ProgramJSON(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.key_tilt = source["key_tilt"];
	        this.positional_xfade = source["positional_xfade"];
	        this.num_keygroups = source["num_keygroups"];
	        this.midi_program_number = source["midi_program_number"];
	        this.enable_midi_program = source["enable_midi_program"];
	        this.samples = this.convertValues(source["samples"], SampleSpec);
	        this.keygroups = this.convertValues(source["keygroups"], KeygroupJSON);
	        this._raw_header_hex = source["_raw_header_hex"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SampleParams {
	    Raw: number[];
	    Name: string;
	    TotalWords: number;
	    SampleRateHz: number;
	    NominalPitch: number;
	    LoudOffset: number;
	    ReplayMode: number;
	    End: number;
	    Start: number;
	    LoopLength: number;
	    VelXFade: number;
	    Reversed: number;
	
	    static createFrom(source: any = {}) {
	        return new SampleParams(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Raw = source["Raw"];
	        this.Name = source["Name"];
	        this.TotalWords = source["TotalWords"];
	        this.SampleRateHz = source["SampleRateHz"];
	        this.NominalPitch = source["NominalPitch"];
	        this.LoudOffset = source["LoudOffset"];
	        this.ReplayMode = source["ReplayMode"];
	        this.End = source["End"];
	        this.Start = source["Start"];
	        this.LoopLength = source["LoopLength"];
	        this.VelXFade = source["VelXFade"];
	        this.Reversed = source["Reversed"];
	    }
	}

}

