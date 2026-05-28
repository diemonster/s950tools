<script lang="ts">
  // Top-level component — picks the right route. The SysEx wire-log
  // is hosted inside Statusbar.svelte so its toggle sits in the
  // statusbar rather than floating over page content.
  import { route } from './lib/route';
  import Program from './routes/Program.svelte';
  import Keygroup from './routes/Keygroup.svelte';
  import Sample from './routes/Sample.svelte';
</script>

{#if $route === 'program'}
  <Program />
{:else if $route === 'keygroup'}
  <Keygroup />
{:else if $route === 'sample'}
  <Sample />
{/if}
