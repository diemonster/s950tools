<script lang="ts">
  // Hints + a right-aligned status line. Pages pass hints[] and
  // status string. Kept dumb on purpose so each page can show
  // context-appropriate shortcuts.
  export let hints: Array<{ key: string; label: string }> = [];
  export let status: string = '';
</script>

<div class="statusbar">
  {#each hints as h}
    <span class="statusbar__hint"><kbd>{h.key}</kbd>{h.label}</span>
  {/each}
  <span class="statusbar__spacer"></span>
  {#if status}<span>{status}</span>{/if}
</div>
