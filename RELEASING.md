# Releasing s950-tools

Releases are built and published by `.github/workflows/release.yml`.
Manual build/signing steps (for local one-offs) stay documented in
BUILDING.md — the workflow automates exactly those.

## Cutting a release

```bash
git tag v0.1.0
git push origin v0.1.0
```

That's it. The workflow then:

1. Runs the full test suite (Go + frontend) as a gate.
2. Builds natively on three runners — the MIDI driver (vendored
   RtMidi C++ via cgo) rules out cross-compilation:
   - **macOS arm64** (Apple Silicon): `s950-gui.app` + CLI, signed
     and notarized when the secrets below are configured.
   - **Windows amd64**: `s950-gui.exe` + `s950-tools.exe`.
   - **Linux amd64**: GUI + CLI binaries (built against
     webkit2gtk-4.1; users need `libwebkit2gtk-4.1`, `libgtk-3`,
     and `libasound2` runtime packages).
3. Publishes one GitHub release for the tag with all three archives,
   a `SHA256SUMS` file, and auto-generated release notes.

To test the pipeline without publishing, use **Run workflow**
(workflow_dispatch) in the Actions tab — it builds and uploads the
archives as workflow artifacts but skips the release step.

## macOS signing + notarization secrets

Configured under **Settings → Secrets and variables → Actions**.
When `MACOS_CERTIFICATE` is absent the macOS job still runs and
ships an *unsigned* .app (recipients use right-click → Open; see
BUILDING.md), so forks and early testing work without any of this.

| Secret | Contents |
| --- | --- |
| `MACOS_CERTIFICATE` | Base64 of the Developer ID Application certificate exported as `.p12` (see below) |
| `MACOS_CERTIFICATE_PASSWORD` | The password chosen during the `.p12` export |
| `APPLE_SIGNING_IDENTITY` | The certificate's full name, e.g. `Developer ID Application: Your Name (ABCDE12345)` |
| `APPLE_ID` | The Apple ID email of the developer account |
| `APPLE_TEAM_ID` | The 10-character team ID from developer.apple.com → Membership |
| `APPLE_APP_SPECIFIC_PASSWORD` | App-specific password generated at appleid.apple.com → Sign-In and Security |

### Exporting the certificate

In Keychain Access on the Mac that holds the **Developer ID
Application** certificate (NOT "Apple Development" — that one can't
distribute outside the App Store):

1. My Certificates → right-click the cert → Export… → `.p12`,
   choose an export password (→ `MACOS_CERTIFICATE_PASSWORD`).
2. Base64 it for the secret:

   ```bash
   base64 -i DeveloperID.p12 | pbcopy   # → MACOS_CERTIFICATE
   ```

3. The exact identity string:

   ```bash
   security find-identity -v -p codesigning | grep "Developer ID Application"
   ```

### What the workflow does with them

A throwaway keychain (random password, never stored) imports the
cert; the .app and the CLI binary are signed with hardened runtime +
timestamp; both are submitted to `notarytool --wait`; the .app gets
the ticket stapled (plain binaries can't be stapled — Gatekeeper
checks the CLI's ticket online via its signature).

## Versioning notes

- Artifact names carry the tag verbatim:
  `s950-tools_v0.1.0_macos_arm64.zip` etc.
- macOS builds are Apple-Silicon-only by design (the Intel slice
  doubles the binary for hardware Apple stopped selling in 2020).
  If an Intel request ever comes in, change the wails platform to
  `darwin/universal` in the workflow — everything else (signing,
  notarization, packaging) is identical.
