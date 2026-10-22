<script lang="ts" module>
  // Line icons drawn for this application (24 × 24 grid, stroke only): no
  // icon font and no package, the binary must stay self-contained.
  const shapes: Record<string, string> = {
    home: '<rect x="3" y="3" width="7.5" height="7.5" rx="1.5"/><rect x="13.5" y="3" width="7.5" height="7.5" rx="1.5"/><rect x="3" y="13.5" width="7.5" height="7.5" rx="1.5"/><rect x="13.5" y="13.5" width="7.5" height="7.5" rx="1.5"/>',
    topology:
      '<circle cx="12" cy="5" r="2.5"/><circle cx="5" cy="19" r="2.5"/><circle cx="19" cy="19" r="2.5"/><path d="M12 7.5V12M12 12l-5.3 5.2M12 12l5.3 5.2"/>',
    diff: '<path d="M4 8h13M13 4l4 4-4 4M20 16H7M11 12l-4 4 4 4"/>',
    losses: '<path d="M12 3.5 2.5 20h19L12 3.5z"/><path d="M12 10v4.5M12 17.2v.1"/>',
    events: '<path d="M8.5 6H21M8.5 12H21M8.5 18H21M3.5 6h.1M3.5 12h.1M3.5 18h.1"/>',
    flows: '<path d="M3 7h3c6 0 6 10 12 10h3M3 17h3c6 0 6-10 12-10h3M18.5 4.5 21 7l-2.5 2.5M18.5 14.5 21 17l-2.5 2.5"/>',
    constraints: '<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V7.5a4 4 0 0 1 8 0V11"/>',
    sliders:
      '<path d="M4 6h8M16 6h4M4 12h2M10 12h10M4 18h10M18 18h2"/><circle cx="14" cy="6" r="2"/><circle cx="8" cy="12" r="2"/><circle cx="16" cy="18" r="2"/>',
    dc: '<rect x="5" y="3" width="14" height="18" rx="1.5"/><path d="M9 7.5h1.5M13.5 7.5H15M9 11.5h1.5M13.5 11.5H15M10 21v-4h4v4"/>',
    tier: '<path d="M12 3.5 3 8l9 4.5L21 8l-9-4.5zM3 12.5l9 4.5 9-4.5M3 16.5 12 21l9-4.5"/>',
    machine:
      '<rect x="3" y="4" width="18" height="7" rx="1.5"/><rect x="3" y="13" width="18" height="7" rx="1.5"/><path d="M7 7.5h.1M7 16.5h.1M11 7.5h6M11 16.5h6"/>',
    node: '<rect x="6.5" y="6.5" width="11" height="11" rx="2"/><path d="M10 3.5v3M14 3.5v3M10 17.5v3M14 17.5v3M3.5 10h3M3.5 14h3M17.5 10h3M17.5 14h3"/>',
    index:
      '<ellipse cx="12" cy="6" rx="8" ry="3"/><path d="M4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3"/>',
    family: '<path d="M3 7a1.5 1.5 0 0 1 1.5-1.5H9l2 2.5h8.5A1.5 1.5 0 0 1 21 9.5v8A1.5 1.5 0 0 1 19.5 19h-15A1.5 1.5 0 0 1 3 17.5V7z"/>',
    shard: '<rect x="8.5" y="8.5" width="12" height="12" rx="2"/><path d="M15.5 8.5V5.5a2 2 0 0 0-2-2h-8a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h3"/>',
    globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c3.2 3.5 3.2 14.5 0 18M12 3c-3.2 3.5-3.2 14.5 0 18"/>',
    clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3.5 2"/>',
    chart: '<path d="M3.5 3.5v17h17M7.5 15l4-5 3 3 5-7"/>',
    tag: '<path d="M3.5 11.5v-7a1 1 0 0 1 1-1h7l9 9-8 8-9-9z"/><path d="M8 8h.1"/>',
    path: '<circle cx="5" cy="18" r="2"/><circle cx="19" cy="6" r="2"/><path d="M7 18h8.5a3 3 0 0 0 0-6h-7a3 3 0 0 1 0-6H17"/>',
    activity: '<path d="M3 12h4l3-8 4 16 3-8h4"/>',
    disk: '<path d="M3 14.5 6 5h12l3 9.5V19H3v-4.5zM3 14.5h18M7 16.8h.1"/>',
    users:
      '<circle cx="9" cy="8" r="3.5"/><path d="M2.5 20c0-3.5 2.9-6 6.5-6s6.5 2.5 6.5 6M16 4.7a3.5 3.5 0 0 1 0 6.6M18 14.5c2.2.8 3.5 2.9 3.5 5.5"/>',
    shield: '<path d="M12 3 4.5 6v6c0 4.5 3.2 8 7.5 9 4.3-1 7.5-4.5 7.5-9V6L12 3z"/><path d="M9 12l2.2 2.2L15 10.2"/>',
    monitor: '<rect x="3" y="4" width="18" height="13" rx="2"/><path d="M8 21h8M12 17v4"/>',
    zoomout: '<circle cx="10.5" cy="10.5" r="6.5"/><path d="M15.5 15.5 20.5 20.5M7.5 10.5h6"/>',
    plus: '<circle cx="12" cy="12" r="9"/><path d="M12 8v8M8 12h8"/>',
    minus: '<circle cx="12" cy="12" r="9"/><path d="M8 12h8"/>',
    check: '<circle cx="12" cy="12" r="9"/><path d="M8 12.3l2.7 2.7L16 9.5"/>',
    cross: '<circle cx="12" cy="12" r="9"/><path d="M9 9l6 6M15 9l-6 6"/>',
    up: '<circle cx="12" cy="12" r="9"/><path d="M12 16.5v-9M8.5 11 12 7.5 15.5 11"/>',
    question: '<circle cx="12" cy="12" r="9"/><path d="M9.5 9.5a2.5 2.5 0 1 1 3.5 2.3c-.7.4-1 .9-1 1.7M12 16.8h.1"/>',
    star: '<path d="M12 3.5l2.6 5.4 5.9.8-4.3 4.1 1 5.9L12 16.9l-5.2 2.8 1-5.9-4.3-4.1 5.9-.8L12 3.5z"/>',
  };

  // What each place of the application shows → its shape.
  const alias: Record<string, string> = {
    cluster: 'topology',
    admin: 'sliders',
    'group.attr': 'dc',
    'group.cluster': 'topology',
    'group.tier': 'tier',
    'group.machine': 'machine',
    'admin.clusters': 'topology',
    'admin.collectors': 'activity',
    'admin.storage': 'disk',
    'admin.settings': 'sliders',
    'admin.auth': 'users',
    'admin.ha': 'shield',
    'admin.system': 'monitor',
    'diff.path': 'path',
    'diff.nodes': 'node',
    'diff.indices': 'index',
    'diff.shards': 'shard',
    'diff.settings': 'sliders',
    'diff.cluster_settings': 'sliders',
    'diff.remotes': 'globe',
    'sum.nodes_added': 'node',
    'sum.nodes_removed': 'node',
    'sum.nodes_changed': 'node',
    'sum.indices_added': 'index',
    'sum.indices_removed': 'index',
    'sum.indices_changed': 'index',
    'sum.shards_moved': 'flows',
    'sum.shards_lost': 'losses',
    'sum.shards_new': 'shard',
    'sum.shards_promoted': 'up',
    'sum.settings_changed': 'sliders',
    'sum.cluster_settings_changed': 'sliders',
    'sum.remotes_changed': 'globe',
    'losses.lost': 'losses',
    'losses.primaries_lost': 'star',
    'losses.not_promoted': 'cross',
    'losses.promoted': 'up',
    'losses.reallocated': 'check',
    'losses.still_unassigned': 'question',
    'losses.deleted': 'index',
  };
</script>

<script lang="ts">
  let { name, size = 16 }: { name: string; size?: number } = $props();
  const shape = $derived(shapes[alias[name] ?? name]);
</script>

{#if shape}<svg viewBox="0 0 24 24" width={size} height={size} aria-hidden="true">{@html shape}</svg>{/if}

<style>
  svg {
    fill: none;
    stroke: currentColor;
    stroke-width: 1.8;
    stroke-linecap: round;
    stroke-linejoin: round;
    vertical-align: -0.16em;
    flex: none;
    opacity: 0.85;
  }
</style>
