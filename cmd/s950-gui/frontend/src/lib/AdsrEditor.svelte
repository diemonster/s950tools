<script lang="ts">
  // ADSR envelope editor — four draggable nodes (Attack peak, Decay
  // corner, Sustain level, Release end). Each phase has its own
  // x-budget that grows monotonically with its value, so dragging
  // the Release node right *visibly* lengthens release (no more
  // pinning to the right edge).
  //
  // Layout in the 200×50 viewBox:
  //   0 ──────── ax ─── ax+dx ────── ax+dx+SUSTAIN_W ──── ax+dx+SUSTAIN_W+rx
  //   │         (Att)  (D→S)       (sustain held)        (Release end)
  // Empty space to the right is drawn as a flat baseline so the
  // curve doesn't look amputated when R is short.

  import { createEventDispatcher } from 'svelte';

  export let a: number;
  export let d: number;
  export let s: number;
  export let r: number;
  export let stroke = '#FDF000';

  const dispatch = createEventDispatcher<{
    change: { a?: number; d?: number; s?: number; r?: number };
  }>();

  const W = 200, H = 50, TOP = 4;
  const A_MAX = 40, D_MAX = 40, R_MAX = 60;
  const SUSTAIN_W = 50; // fixed visual width for the hold

  const clamp = (lo: number, hi: number, v: number) =>
    Math.max(lo, Math.min(hi, v));

  // Layout positions in viewBox space.
  $: ax = (a / 99) * A_MAX;
  $: dx = (d / 99) * D_MAX;
  $: rx = (r / 99) * R_MAX;
  $: sy = H - (s / 99) * (H - TOP);
  $: sustainStart = ax + dx;
  $: sustainEnd   = sustainStart + SUSTAIN_W;
  $: releaseEnd   = sustainEnd + rx;

  // Path: attack ramp → decay → sustain → release → flat baseline.
  $: path =
    `M 0 ${H} ` +
    `L ${ax.toFixed(1)} ${TOP} ` +
    `L ${sustainStart.toFixed(1)} ${sy.toFixed(1)} ` +
    `L ${sustainEnd.toFixed(1)} ${sy.toFixed(1)} ` +
    `L ${releaseEnd.toFixed(1)} ${H} ` +
    `L ${W} ${H}`;

  // ---------- Drag handling ----------
  let svgEl: SVGSVGElement | undefined;
  type Node = 'a' | 'd' | 's' | 'r' | null;
  let dragNode: Node = null;

  function onNodeDown(e: MouseEvent, node: Node) {
    if (e.button !== 0) return;
    dragNode = node;
    e.preventDefault();
    e.stopPropagation();
  }

  function onWindowMove(e: MouseEvent) {
    if (!dragNode || !svgEl) return;
    const rect = svgEl.getBoundingClientRect();
    const px = ((e.clientX - rect.left) / rect.width) * W;
    const py = ((e.clientY - rect.top) / rect.height) * H;

    if (dragNode === 'a') {
      const newA = clamp(0, 99, Math.round((px / A_MAX) * 99));
      dispatch('change', { a: newA });
    } else if (dragNode === 'd') {
      // D corner is 2D: x = decay time (measured from the attack
      // peak so the node stays trackable when A shifts), y =
      // sustain level. Matches the convention in most DAW envelope
      // editors — one handle for the "decay reaches sustain" point.
      const newD = clamp(0, 99, Math.round(((px - ax) / D_MAX) * 99));
      const newS = clamp(0, 99, Math.round(((H - py) / (H - TOP)) * 99));
      dispatch('change', { d: newD, s: newS });
    } else if (dragNode === 's') {
      // Sustain is a level (y only); x is fixed at sustainEnd — the
      // held segment has no time parameter in ADSR. Redundant with
      // the y-axis of the D corner so you can grab either side.
      const newS = clamp(0, 99, Math.round(((H - py) / (H - TOP)) * 99));
      dispatch('change', { s: newS });
    } else if (dragNode === 'r') {
      // R is the distance from sustainEnd to the release endpoint.
      const newR = clamp(0, 99, Math.round(((px - sustainEnd) / R_MAX) * 99));
      dispatch('change', { r: newR });
    }
  }

  function onWindowUp() {
    dragNode = null;
  }
</script>

<svelte:window on:mousemove={onWindowMove} on:mouseup={onWindowUp} />

<div class="adsr">
  <svg bind:this={svgEl} viewBox="0 0 200 50" preserveAspectRatio="none">
    <path d={path} fill="none" stroke={stroke} stroke-width="2" />

    <!-- Hit rings (transparent, larger) for easier grabbing; visible
         dots on top for the affordance. -->
    <circle class="hit" cx={ax}            cy={TOP} r="6" on:mousedown={(e) => onNodeDown(e, 'a')} />
    <circle class="dot" cx={ax}            cy={TOP} r="3" fill={stroke} />

    <circle class="hit" cx={sustainStart}  cy={sy}  r="6" on:mousedown={(e) => onNodeDown(e, 'd')} />
    <circle class="dot" cx={sustainStart}  cy={sy}  r="3" fill={stroke} />

    <circle class="hit" cx={sustainEnd}    cy={sy}  r="6" on:mousedown={(e) => onNodeDown(e, 's')} />
    <circle class="dot" cx={sustainEnd}    cy={sy}  r="3" fill={stroke} />

    <circle class="hit" cx={releaseEnd}    cy={H}   r="6" on:mousedown={(e) => onNodeDown(e, 'r')} />
    <circle class="dot" cx={releaseEnd}    cy={H}   r="3" fill={stroke} />
  </svg>
</div>

<style>
  .adsr {
    height: 64px;
    border: var(--bw) solid var(--black);
    border-radius: var(--r);
    background: var(--canvas);
    position: relative;
    overflow: hidden;
  }
  svg { display: block; width: 100%; height: 100%; }
  .dot { pointer-events: none; }
  .hit { fill: transparent; cursor: grab; }
  .hit:active { cursor: grabbing; }
</style>
