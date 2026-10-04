<script lang="ts">
  // "i" icon carrying the explanation of a zone (hover or keyboard focus).
  import { tip } from '../lib/tip';

  let { text }: { text: string } = $props();
</script>

<!-- Focusable so that keyboard users can read the explanation; not a button: it does nothing on click. -->
<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<span class="info" tabindex="0" role="note" aria-label={text} use:tip={text}>i</span>

<style>
  .info {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 1.15rem;
    height: 1.15rem;
    margin-left: 0.3rem;
    border-radius: 50%;
    border: 1px solid var(--muted);
    color: var(--muted);
    font: italic 600 0.78rem/1 Georgia, 'Times New Roman', serif;
    cursor: help;
    vertical-align: middle;
    user-select: none;
    flex: none;
  }
  .info:hover,
  .info:focus-visible {
    border-color: var(--accent);
    color: var(--accent);
    outline: none;
  }
</style>
