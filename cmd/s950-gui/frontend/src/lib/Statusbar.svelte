<script lang="ts">
  // Hints + a right-aligned status line. Pages pass hints[] and
  // status string. Kept dumb on purpose so each page can show
  // context-appropriate shortcuts. Also hosts the SysEx wire-log
  // toggle on the right so it never floats over page content.
  import WireLog from './WireLog.svelte';
  export let hints: Array<{ key: string; label: string }> = [];
  export let status: string = '';
</script>

<div class="statusbar">
  {#each hints as h}
    <span class="statusbar__hint"><kbd>{h.key}</kbd>{h.label}</span>
  {/each}
  <span class="statusbar__spacer"></span>
  {#if status}<span class="statusbar__status">{status}</span>{/if}
  <WireLog />
</div>
