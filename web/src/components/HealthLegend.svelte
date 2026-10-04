<script lang="ts">
  // Legend of the health band (HealthBand).
  import { t } from '../lib/i18n';
</script>

<div class="legend">
  <span><span class="sw" style:background="var(--green)"></span>{t('legend.green')}</span>
  <span><span class="sw" style:background="var(--yellow)"></span>{t('legend.yellow')}</span>
  <span><span class="sw" style:background="var(--red)"></span>{t('legend.red')}</span>
  <span><span class="sw hatch"></span>{t('legend.unknown')}</span>
  <span><span class="sw bar"></span>{t('legend.events')}</span>
  <span><span class="sw bar severe"></span>{t('legend.severe')}</span>
</div>

<style>
  .hatch {
    background: repeating-linear-gradient(45deg, var(--grey) 0 2px, var(--surface-2) 2px 4px);
  }
  .bar {
    width: 0.35rem !important;
    background: var(--muted);
  }
  .severe {
    background: var(--red);
  }
</style>
