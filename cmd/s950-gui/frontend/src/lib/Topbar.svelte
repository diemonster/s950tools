<script lang="ts">
  import { route, navigate, type Route } from './route';
  import { sync } from './sync';

  // Slot count chip text is page-specific (programs vs samples) — let
  // the parent route override it via prop.
  export let slotChip: string = '';

  $: chipClass = (() => {
    const state = $sync.state;
    return state === 'synced'
      ? 'chip--status'
      : `chip--status chip--${state}`;
  })();

  function tabClass(name: Route): string {
    return name === $route ? 'tab active' : 'tab';
  }
</script>

<header class="topbar">
  <div class="logo">s950-tools</div>
  <nav class="tabs">
    <button class={tabClass('program')}  on:click={() => navigate('program')}>Program</button>
    <button class={tabClass('keygroup')} on:click={() => navigate('keygroup')}>Keygroup</button>
    <button class={tabClass('sample')}   on:click={() => navigate('sample')}>Sample</button>
  </nav>
  <div class="spacer"></div>

  <!-- TODO(midi-ports): wire to Wails-side rtmidi port enumeration. -->
  <span class="chip">
    <span class="chip__label">MIDI in</span>
    <strong>MRCC Port 03 ▾</strong>
  </span>
  <span class="chip">
    <span class="chip__label">MIDI out</span>
    <strong>MRCC Port 03 ▾</strong>
  </span>
  <span class="chip">Ch · 0 ▾</span>

  <span class={chipClass}><span class="status-dot"></span>{$sync.label}</span>
  {#if slotChip}
    <span class="chip accent">{slotChip}</span>
  {/if}
</header>
