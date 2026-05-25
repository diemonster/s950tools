#!/usr/bin/env bash
# Regenerate the Wails app icon from the source SVG. Run from repo
# root: `make icon`. Uses macOS' built-in `qlmanage` for the
# SVG→PNG render (no extra dependencies); other platforms can swap
# in `rsvg-convert` or `inkscape --export-png` against the same
# wrapped intermediate.
#
# Why the wrapper SVG: the source artwork has its own width/height
# (187×168, non-square) and clip-paths sized to that bound. Letting
# qlmanage render it directly produces a top-left-anchored result
# inside the square output frame. Wrapping the artwork in a 224×224
# (square) outer SVG with a translate(18.5, 28) centring transform
# yields a properly centred 1024×1024 PNG, the Wails-expected shape.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$REPO_ROOT/cmd/s950-gui/frontend/src/assets/images/Brain Dude-02.svg"
OUT="$REPO_ROOT/cmd/s950-gui/build/appicon.png"
TMP_WRAP="$(mktemp -t icon-wrap).svg"

if [[ ! -f "$SRC" ]]; then
  echo "✗ Source SVG missing: $SRC" >&2
  exit 1
fi
if ! command -v qlmanage >/dev/null 2>&1; then
  echo "✗ qlmanage not found (macOS-only). Install librsvg or inkscape on other platforms and adjust this script." >&2
  exit 1
fi

# Wrap: strip the source's outer <svg ...>…</svg> tags and re-host
# the content inside a square 224×224 viewBox, translated to centre
# the 187×168 artwork. Constants here mirror the source SVG's
# native dimensions; bump them only if the source artwork itself
# changes size.
{
  echo '<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 224 224">'
  echo '<g transform="translate(18.5, 28)">'
  awk 'BEGIN{p=0}
       /<svg[^>]*>/ { p=1; sub(/.*<svg[^>]*>/, ""); if($0!="") print; next }
       /<\/svg>/    { p=0; sub(/<\/svg>.*/, ""); if($0!="") print; next }
       p' "$SRC"
  echo '</g></svg>'
} > "$TMP_WRAP"

# qlmanage caches by content; force a clean render.
rm -f "${TMP_WRAP}.png"
qlmanage -t -s 1024 "$TMP_WRAP" -o "$(dirname "$TMP_WRAP")" >/dev/null 2>&1

if [[ ! -f "${TMP_WRAP}.png" ]]; then
  echo "✗ qlmanage produced no output" >&2
  exit 1
fi

mv "${TMP_WRAP}.png" "$OUT"
rm -f "$TMP_WRAP"

echo "✓ appicon.png  $(sips -g pixelWidth -g pixelHeight "$OUT" | awk '/pixel/ {printf $2" "}')"

# Windows .ico — Wails v2 only auto-generates the macOS .icns from
# appicon.png on build, so we regenerate the .ico here using the
# self-contained scripts/png2ico tool. CatmullRom resampling keeps
# the 16/24/32px sizes sharp.
ICO_OUT="$REPO_ROOT/cmd/s950-gui/build/windows/icon.ico"
( cd "$REPO_ROOT/scripts/png2ico" && go run . "$OUT" "$ICO_OUT" )

echo "  Run \`make build-gui\` to fold these into the .app / .exe bundle."
