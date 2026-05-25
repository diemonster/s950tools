# Building and distributing s950-tools

This document covers builds beyond local development. For the dev
loop (`wails dev`, `make test`, `make doctor`), see the README +
`make help`.

## Prerequisites

`make doctor` verifies everything is installed at the right version.
The full required set:

- **Go ≥1.24.2** — backend + Wails build. <https://go.dev/dl/>
- **Node + npm** — frontend (Vite + Svelte + Vitest). LTS is fine.
- **`wails`** — desktop app build CLI: `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- **`staticcheck`** — Go lint: `go install honnef.co/go/tools/cmd/staticcheck@latest`

Mac-only distribution work additionally needs Xcode Command Line
Tools (`xcode-select --install`) for `codesign`, `xcrun stapler`,
and `xcrun notarytool`.

## Build targets

| Command                     | Output                                                | When                                  |
| --------------------------- | ----------------------------------------------------- | ------------------------------------- |
| `make build-cli`            | `./s950-tools` (CLI binary, host arch)                | Quick CLI test                        |
| `make build-gui`            | `cmd/s950-gui/build/bin/s950-gui.app` (host arch only)| Local dev / personal use              |
| `make build-gui-universal`  | Same path, universal binary (arm64 + amd64)           | Anything you plan to share with others|
| `make icon`                 | Regenerates `appicon.png` + `icon.ico` from the SVG   | After the source artwork changes      |

The host-arch GUI build is ~70 MB; the universal build is ~120 MB.
Use universal only when you're sending the app to someone else —
otherwise the extra slice is wasted bytes.

### Build environment

The Makefile sets two cgo env vars for every macOS build:

- `MACOSX_DEPLOYMENT_TARGET=11.0` — aligns the cgo SDK target with
  the Wails linker default so the build doesn't emit *"object file
  built for newer macOS version"* warnings. 11.0 is the
  Apple-Silicon-cutover floor; older versions are out of Apple
  security support anyway.
- `CGO_CXXFLAGS="-Wno-vla-cxx-extension"` — silences a benign
  variable-length-array warning from the vendored RtMidi C++ code
  that we can't patch upstream.

If you ever lower the minimum macOS version (e.g. to ship to older
hardware), bump `MAC_BUILD_ENV` in the Makefile, not the individual
recipes — both `build-cli` and the GUI targets pick it up.

## Distributing the macOS `.app` bundle

A `wails build` output is a fully self-contained `.app`. The MIDI
driver (rtmidi) is compiled in via cgo, so the recipient doesn't
need to install anything. There are two ways to ship it:

### Casual sharing (zero cost, friction on first launch)

1. `make build-gui-universal`
2. Zip the .app: `ditto -c -k --keepParent cmd/s950-gui/build/bin/s950-gui.app /tmp/s950-tools.zip`
3. Send the zip.

On the recipient's machine, the first launch hits **Gatekeeper**
because the bundle isn't signed by a recognised developer. They'll
see *"s950-gui cannot be opened because the developer cannot be
verified"*. To bypass:

- **Right-click the .app → Open** → click *Open* in the warning
  dialog (NOT double-click — that gives no escape hatch).
- Alternative: System Settings → Privacy & Security → scroll to
  "s950-gui was blocked..." → click *Open Anyway*.

Once approved, future launches are normal.

### Signed + notarized (clean install, $99/year cost)

For a no-warnings experience, the app must be signed with a
**Developer ID Application** certificate (Apple Developer Program
membership, $99/year) and notarized through Apple. Outline:

```bash
# 1. Build the universal bundle.
make build-gui-universal

# 2. Sign every embedded binary + the .app itself with hardened
#    runtime (required for notarization).
codesign --deep --force --verify --options runtime --timestamp \
  --sign "Developer ID Application: Your Name (TEAMID)" \
  cmd/s950-gui/build/bin/s950-gui.app

# 3. Zip for upload. ditto preserves macOS metadata; plain zip
#    strips signatures and breaks notarization.
ditto -c -k --keepParent cmd/s950-gui/build/bin/s950-gui.app \
  /tmp/s950-tools.zip

# 4. Submit to Apple notary service (typically completes in 1–5min).
#    `--password` accepts an app-specific password generated at
#    appleid.apple.com; --team-id is the 10-char ID from your
#    developer account.
xcrun notarytool submit /tmp/s950-tools.zip \
  --apple-id "you@example.com" \
  --team-id  "ABCDEFGHIJ" \
  --password "xxxx-xxxx-xxxx-xxxx" \
  --wait

# 5. Staple the notary ticket to the .app so Gatekeeper validates
#    offline.
xcrun stapler staple cmd/s950-gui/build/bin/s950-gui.app

# 6. Re-zip the stapled .app for distribution.
ditto -c -k --keepParent cmd/s950-gui/build/bin/s950-gui.app \
  /tmp/s950-tools-signed.zip
```

The Wails docs have the canonical reference if any of the above
flags drift between Xcode versions:
<https://wails.io/docs/guides/signing>.

## MIDI permissions on the recipient's machine

The first time the app touches CoreMIDI to enumerate ports, macOS
may prompt for permission depending on the recipient's OS version.
Auto-allowed in 13+; in earlier versions it shows a one-time dialog.
No code changes needed on our side — `rtmidi` is making the call,
and we inherit the OS prompt.

## Windows / Linux builds

Cross-compiling Wails for Windows + Linux from a Mac host works
but needs extra toolchains (mingw-w64 for Windows; gtk/webkit2gtk
headers for Linux). Not currently exercised. If we ever ship on
those platforms, `wails build -platform windows/amd64` and
`wails build -platform linux/amd64` are the entry points. The
Windows icon at `cmd/s950-gui/build/windows/icon.ico` is
pre-generated by `make icon` and consumed automatically.
