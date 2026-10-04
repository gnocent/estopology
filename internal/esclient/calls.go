package esclient

import (
	"context"
	"io"
	"strings"
)

// Request paths (SPEC §5.1, §5.2). scripts/capture-fixtures.sh records the
// real answers of these exact requests for 7.17, 8.x and 9.x.
const (
	PathRoot   = "/"
	PathHealth = "/_cluster/health"

	nodesStatsFilter = "cluster_name,_nodes,nodes.*.name,nodes.*.host,nodes.*.ip,nodes.*.timestamp,nodes.*.roles," +
		"nodes.*.os.cpu,nodes.*.os.cgroup,nodes.*.process.cpu,nodes.*.jvm.uptime_in_millis," +
		"nodes.*.jvm.mem.heap_used_percent,nodes.*.fs.total,nodes.*.fs.io_stats.total"
	PathNodesStats = "/_nodes/stats/os,process,jvm,fs?filter_path=" + nodesStatsFilter

	PathAllocation      = "/_cat/allocation?format=json&bytes=b"
	PathClusterSettings = "/_cluster/settings?flat_settings=true"
	// Defaults are needed for the allocation constraints (watermarks, awareness).
	PathClusterSettingsDefaults = "/_cluster/settings?flat_settings=true&include_defaults=true"
	PathRemoteInfo              = "/_remote/info"

	// _nodes (total, successful, failed) tells whether every node answered.
	nodesFilter = "cluster_name,_nodes,nodes.*.name,nodes.*.host,nodes.*.ip,nodes.*.version,nodes.*.roles,nodes.*.attributes," +
		"nodes.*.transport_address,nodes.*.http.publish_address,nodes.*.os.pretty_name,nodes.*.os.name," +
		"nodes.*.os.version,nodes.*.os.arch,nodes.*.os.available_processors,nodes.*.jvm.version,nodes.*.jvm.start_time_in_millis"
	PathNodes = "/_nodes/os,jvm,http,transport?filter_path=" + nodesFilter

	PathCatIndices = "/_cat/indices?format=json&bytes=b&expand_wildcards=all" +
		"&h=uuid,index,status,health,pri,rep,docs.count,store.size,pri.store.size,creation.date"
	PathCatShards = "/_cat/shards?format=json&bytes=b" +
		"&h=index,shard,prirep,state,docs,store,id,node,unassigned.reason,unassigned.at,unassigned.details"

	PathHasPrivileges = "/_security/user/_has_privileges"
)

// DefaultSettingsWhitelist is the default list of index settings read at
// each full capture (SPEC §5.7); `index.routing.allocation.*` covers
// include/exclude/require, total_shards_per_node, enable and _tier_preference.
var DefaultSettingsWhitelist = []string{
	"index.number_of_shards", "index.number_of_replicas", "index.auto_expand_replicas",
	"index.routing.allocation.*", "index.routing.rebalance.enable", "index.blocks.*",
	"index.unassigned.node_left.delayed_timeout", "index.priority", "index.refresh_interval",
	"index.translog.durability", "index.lifecycle.name", "index.version.created",
	"index.creation_date", "index.uuid", "index.hidden",
}

// IndexSettingsPath builds the index settings request for a whitelist.
func IndexSettingsPath(whitelist []string) string {
	// Setting names are plain ASCII (dots, underscores, '*'), sent as is.
	return "/_all/_settings/" + strings.Join(whitelist, ",") + "?flat_settings=true&expand_wildcards=all"
}

// RequiredPrivileges is the body of the privilege check (SPEC §5.6).
const RequiredPrivileges = `{"cluster":["monitor"],"index":[{"names":["*"],` +
	`"privileges":["monitor","view_index_metadata"],"allow_restricted_indices":true}]}`

// Root calls GET /.
func (c *Client) Root(ctx context.Context) (v Root, st CallStat, err error) {
	st, err = c.Get(ctx, "root", PathRoot, nil, func(r io.Reader) (e error) { v, e = ParseRoot(r); return })
	return
}

// Health calls GET _cluster/health.
func (c *Client) Health(ctx context.Context) (v Health, st CallStat, err error) {
	st, err = c.Get(ctx, "health", PathHealth, nil, func(r io.Reader) (e error) { v, e = ParseHealth(r); return })
	return
}

// NodesStats calls GET _nodes/stats/os,process,jvm,fs.
func (c *Client) NodesStats(ctx context.Context) (v []NodeStats, st CallStat, err error) {
	st, err = c.Get(ctx, "nodes_stats", PathNodesStats, nil, func(r io.Reader) (e error) { v, e = ParseNodesStats(r); return })
	return
}

// Allocation calls GET _cat/allocation.
func (c *Client) Allocation(ctx context.Context) (v []Allocation, st CallStat, err error) {
	st, err = c.Get(ctx, "cat_allocation", PathAllocation, nil, func(r io.Reader) (e error) { v, e = ParseAllocation(r); return })
	return
}

// ClusterSettings calls GET _cluster/settings, with defaults if asked.
func (c *Client) ClusterSettings(ctx context.Context, withDefaults bool) (v ClusterSettings, st CallStat, err error) {
	name, path := "cluster_settings", PathClusterSettings
	if withDefaults {
		name, path = "cluster_settings_defaults", PathClusterSettingsDefaults
	}
	st, err = c.Get(ctx, name, path, nil, func(r io.Reader) (e error) { v, e = ParseClusterSettings(r); return })
	return
}

// RemoteInfo calls GET _remote/info. ES answers 400 when the node handling
// the request lacks the remote_cluster_client role: callers treat that as
// "CCS information unavailable", not as a capture failure.
func (c *Client) RemoteInfo(ctx context.Context) (v []Remote, st CallStat, err error) {
	st, err = c.Get(ctx, "remote_info", PathRemoteInfo, nil, func(r io.Reader) (e error) { v, e = ParseRemoteInfo(r); return })
	return
}

// Nodes calls GET _nodes/os,jvm,http,transport.
func (c *Client) Nodes(ctx context.Context) (v []NodeInfo, st CallStat, err error) {
	st, err = c.Get(ctx, "nodes", PathNodes, nil, func(r io.Reader) (e error) { v, e = ParseNodes(r); return })
	return
}

// CatIndices streams GET _cat/indices.
func (c *Client) CatIndices(ctx context.Context, fn func(CatIndex) error) (CallStat, error) {
	return c.Get(ctx, "cat_indices", PathCatIndices, nil, func(r io.Reader) error { return DecodeCatIndices(r, fn) })
}

// CatShards streams GET _cat/shards.
func (c *Client) CatShards(ctx context.Context, fn func(CatShard) error) (CallStat, error) {
	return c.Get(ctx, "cat_shards", PathCatShards, nil, func(r io.Reader) error { return DecodeCatShards(r, fn) })
}

// IndexSettings streams the whitelisted settings of every index.
func (c *Client) IndexSettings(ctx context.Context, whitelist []string, fn func(string, map[string]string) error) (CallStat, error) {
	return c.Get(ctx, "index_settings", IndexSettingsPath(whitelist), nil, func(r io.Reader) error { return DecodeIndexSettings(r, fn) })
}

// HasPrivileges checks the privileges estopo needs (admin connection test).
func (c *Client) HasPrivileges(ctx context.Context) (v Privileges, st CallStat, err error) {
	st, err = c.Get(ctx, "has_privileges", PathHasPrivileges, []byte(RequiredPrivileges),
		func(r io.Reader) (e error) { v, e = ParsePrivileges(r); return })
	return
}
