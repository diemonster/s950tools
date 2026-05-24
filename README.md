# s950-tools

A small Go CLI for managing samples and programs on an Akai S900/S950 sampler
over MIDI SysEx. It reads the device catalog, fetches sample/program data,
uploads WAV/AIFF audio to sample slots, and writes whole programs (header +
up to 31 keygroups) from a JSON manifest.

## Install / build

```bash
go build ./cmd/s950-tools
```

The build needs a working C toolchain (the MIDI driver wraps RtMidi via cgo).

## CLI usage

All commands share these global flags:

| flag                | default    | meaning                            |
| ------------------- | ---------- | ---------------------------------- |
| `--in <name>`       | first port | MIDI input port (substring match)  |
| `--out <name>`      | first port | MIDI output port (substring match) |
| `--channel <0..15>` | 0          | S950 MIDI channel                  |
| `--timeout <dur>`   | 5s         | per-request timeout                |
| `--verbose`         | off        | hex-dump every SysEx to stderr     |

### Commands

| command                        | what it does                                                                                                          |
| ------------------------------ | --------------------------------------------------------------------------------------------------------------------- |
| `ports`                        | list available MIDI input/output ports                                                                                |
| `catalog`                      | list samples (`SMP`) and programs (`PRG`) on the device                                                               |
| `get-params <num>`             | print one sample's SPRM block (add `--json` for machine-readable)                                                     |
| `get-program <num>`            | dump program N (header + keygroups) as JSON; `-o file.json` to save                                                   |
| `put-program <file.json>`      | upload a program from JSON; if the JSON includes a `samples[]` manifest the referenced audio files are uploaded first |
| `program-template`             | emit a starter program JSON (`--name`, `--keygroups`, `-o`)                                                           |
| `put-sample <in.wav\|in.aiff>` | upload a single audio file to a sample slot (see flags below)                                                         |
| `monitor`                      | listen for inbound SysEx and hex-dump it (`--duration`)                                                               |

### `put-sample` flags

| flag                                   | meaning                                                                                                             |
| -------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `--slot <0..99>`                       | preferred slot; auto-picks lowest empty if occupied                                                                 |
| `--rate <hz\|alias>`                   | force-resample before upload. Aliases: `telephone`, `lofi`, `sp1200`, `mpc60`, and `s950-7/10/12/15/20/26/31/37/40` |
| `--channel-mode left\|right\|mix`      | how to fold stereo to mono                                                                                          |
| `--tune <semitones>`                   | nudge the SPRM nominal pitch (fractional OK)                                                                        |
| `--loop-start`, `--loop-end`, `--mode` | loop config (one-shot when end ≤ start + 5)                                                                         |
| `--max-frames <n>`                     | truncate input to N frames                                                                                          |

### Typical session

```bash
s950-tools ports
s950-tools --out "MRCC Port 03" catalog
s950-tools --out "MRCC Port 03" put-sample --rate sp1200 kick.wav
s950-tools --out "MRCC Port 03" put-program kit.json --slot 2
```

### Program JSON template

[`examples/program.json`](examples/program.json) is an annotated, ready-to-edit
template showing every field `put-program` understands: the program-level
header, an optional `samples[]` manifest (audio files uploaded before the
program), and one fully-populated keygroup block per zone. Use it as a
starting point — copy it next to your audio files, adjust `lower_key` /
`upper_key` / `soft_sample` / `soft_tune` per zone, then upload:

```bash
s950-tools --out "MRCC Port 03" put-program examples/program.json --slot 2
```

If you'd rather start from a blank-but-correct skeleton, `s950-tools
program-template --keygroups N -o new.json` emits the same shape with default
field values and empty sample names. The `_raw_header_hex` and
`_raw_bytes_hex` fields preserve undocumented bytes from the S950 protocol —
leave them alone unless you've round-tripped a program from the device with
`get-program`.

## What it does, briefly

The S900/S950 speaks two MIDI SysEx families:

- **AKAI exclusive** (`F0 47 …`) for catalog, program, and sample-parameter
  read/write.
- **System-exclusive-common** (`F0 7E …`), an SDS-style flow used for the
  raw 12-bit sample data dump.

The project is organized as follows:

- [internal/sysex/](internal/sysex/) — wire-encoding primitives (DB / DW / DD /
  TB / SW, XOR checksum).
- [internal/protocol/](internal/protocol/) — message builders/parsers for
  AKAI-exclusive (`AkaiMessage`, `CatalogEntry`, `SampleParams`, `Program`,
  `Keygroup`) and sample-dump (`SampleDumpHeader`, block encode/decode).
  Also the human-editable `ProgramJSON` schema.
- [internal/sample/](internal/sample/) — WAV/AIFF loading, mono folding,
  linear resampling, and 16-bit PCM ↔ 12-bit offset-binary conversion.
- [internal/transport/](internal/transport/) — MIDI in/out wrapper around
  gomidi/rtmidi that delivers complete SysEx envelopes.
- [internal/device/](internal/device/) — high-level device operations
  (`Catalog`, `GetProgram`, `SetProgram`, `GetParams`, `SetParams`,
  `PutSampleOpenLoop`, slot-picking, NAK collection).
- [cmd/s950-tools/](cmd/s950-tools/) — the cobra CLI that wires it all together.

## References

The protocol implementation is cross-checked against
[`dxzl/akai-s950`](https://github.com/dxzl/akai-s950), which has the most
complete public dump of the S900/S950 PRGHEDR / KEYGROUP / SPRM byte layouts.

## License

MIT — see [LICENSE](LICENSE).
