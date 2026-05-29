#!/usr/bin/env bash
# Capture per-route screenshots of the s950 GUI at several viewport
# sizes to surface CSS clipping/overflow. Starts vite with MOCK_WAILS=1
# (swaps wailsjs bindings for mocked fixtures with realistic content),
# then drives headless Chrome through each (route × viewport) combo.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
FRONTEND="$(cd "$HERE/../.." && pwd)"
OUT="$HERE/out"
PORT=5179
CHROME="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

mkdir -p "$OUT"
rm -f "$OUT"/*.png

if [[ ! -x "$CHROME" ]]; then
  echo "ERR: Chrome not found at $CHROME" >&2
  exit 1
fi

# Make sure no orphan vite is still bound from a prior run — that
# would cause this run to either fail to start OR silently reuse the
# stale process, serving outdated CSS to Chrome.
if lsof -ti ":$PORT" >/dev/null 2>&1; then
  echo "→ Killing leftover process on :$PORT"
  lsof -ti ":$PORT" | xargs kill -9 2>/dev/null || true
  sleep 0.3
fi

# Start vite in the background. exec replaces the subshell with vite
# so VITE_PID points to the actual node process — without exec, vite
# is a grandchild and `kill $VITE_PID` only kills the subshell wrapper.
echo "→ Starting vite on :$PORT (MOCK_WAILS=1)…"
(
  cd "$FRONTEND"
  exec env MOCK_WAILS=1 npx vite --port "$PORT" --strictPort >"$OUT/vite.log" 2>&1
) &
VITE_PID=$!

cleanup() {
  if kill -0 "$VITE_PID" 2>/dev/null; then
    kill "$VITE_PID" 2>/dev/null || true
    wait "$VITE_PID" 2>/dev/null || true
  fi
  # Belt + suspenders: anything still on the port goes too. vite
  # forks an esbuild service that can outlive the parent.
  if lsof -ti ":$PORT" >/dev/null 2>&1; then
    lsof -ti ":$PORT" | xargs kill -9 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

# Wait for the server to respond.
echo "→ Waiting for vite to be ready…"
for i in {1..40}; do
  if curl -fsS "http://localhost:$PORT/" >/dev/null 2>&1; then
    echo "  ready after ${i}×0.5s"
    break
  fi
  sleep 0.5
  if [[ $i -eq 40 ]]; then
    echo "ERR: vite never responded on :$PORT" >&2
    tail -40 "$OUT/vite.log" >&2 || true
    exit 1
  fi
done

# Routes × viewports. The 4 sizes bracket the known breakpoints:
#   1440×900 — default desktop the topbar+chips are tuned for
#   1200×800 — between 1100 and 1280 media-query thresholds
#   1024×720 — typical "small laptop" / half-screen
#    900×700 — at/below the Keygroup 1fr|1fr|1fr column threshold
ROUTES=(program keygroup sample)
SIZES=(1440x900 1200x800 1024x720 900x700)

for route in "${ROUTES[@]}"; do
  for size in "${SIZES[@]}"; do
    w="${size%x*}"
    h="${size#*x}"
    url="http://localhost:$PORT/?mock=1&route=$route"
    png="$OUT/${route}-${size}.png"
    echo "→ Capturing $route @ $size"
    "$CHROME" \
      --headless=old \
      --disable-gpu \
      --no-sandbox \
      --hide-scrollbars=false \
      --force-device-scale-factor=1 \
      --window-size="${w},${h}" \
      --virtual-time-budget=6000 \
      --screenshot="$png" \
      "$url" >/dev/null 2>&1 || {
        echo "  WARN: chrome exited non-zero (screenshot may still have written)"
      }
    if [[ ! -s "$png" ]]; then
      echo "  ERR: no screenshot at $png" >&2
    fi
  done
done

echo "→ Screenshots in $OUT"
ls -la "$OUT"/*.png
