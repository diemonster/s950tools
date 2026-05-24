<script lang="ts">
  // Top-level component — just picks the right route. Each route
  // renders its own .app shell (because the Keygroup tab uses the
  // 5-row .app--canvas variant while Program/Sample use .app--3row).
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
