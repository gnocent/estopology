package derive

import (
	"testing"

	"estopo/internal/model"
)

// highWatermarkConcern counts the nodes above the high watermark.
func highWatermarkConcern(st *model.State) int {
	for _, c := range Assess(st, nil, nil).Concerns {
		if c.Code == ConcernHighWatermark {
			return c.Count
		}
	}
	return 0
}

// From 8.5 the high watermark left at its default is capped by a max
// headroom of 150 GB: a 10 TB disk 95 % full (512 GB free) is below it.
func TestHighWatermarkMaxHeadroom(t *testing.T) {
	const tb = int64(1) << 40
	full := func(version string, mutate func(*model.State)) *model.State {
		st := baseState()
		for i := range st.Nodes {
			st.Nodes[i].Version = version
			st.Nodes[i].Metrics = &model.NodeMetrics{DiskTotal: 10 * tb, DiskUsed: 10*tb - tb/2}
		}
		if mutate != nil {
			mutate(st)
		}
		return st
	}
	cases := []struct {
		name    string
		st      *model.State
		above   bool
		verdict string
	}{
		{"8.19 default", full("8.19.22", nil), false, OK},
		{"8.19 default from the defaults", full("8.19.22", func(st *model.State) {
			st.Defaults = map[string]string{"cluster.routing.allocation.disk.watermark.high": "90%",
				"cluster.routing.allocation.disk.watermark.high.max_headroom": "150gb"}
		}), false, OK},
		{"7.17: no max headroom", full("7.17.29", nil), true, Infeasible},
		{"watermark set: no default headroom", full("8.19.22", func(st *model.State) {
			st.ClusterSettings["cluster.routing.allocation.disk.watermark.high"] = "90%"
		}), true, Infeasible},
		{"headroom disabled in the defaults", full("8.19.22", func(st *model.State) {
			st.Defaults = map[string]string{"cluster.routing.allocation.disk.watermark.high.max_headroom": "-1"}
		}), true, Infeasible},
		{"explicit headroom larger than the free space", full("8.19.22", func(st *model.State) {
			st.ClusterSettings["cluster.routing.allocation.disk.watermark.high.max_headroom"] = "1tb"
		}), true, Infeasible},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := highWatermarkConcern(tc.st)
			if (n > 0) != tc.above {
				t.Errorf("nodes above the high watermark: %d, want above=%v", n, tc.above)
			}
			if v := verdict(t, tc.st, index("i", 1, 1, hot(nil))); v.Status != tc.verdict {
				t.Errorf("verdict %+v, want %s", v, tc.verdict)
			}
		})
	}
}

// threshold_enabled=false disables the disk decider: no node is excluded.
func TestDiskThresholdDisabled(t *testing.T) {
	st := baseState()
	for i := range st.Nodes {
		st.Nodes[i].Metrics.DiskUsed = 990
	}
	st.ClusterSettings["cluster.routing.allocation.disk.threshold_enabled"] = "false"
	if n := highWatermarkConcern(st); n != 0 {
		t.Errorf("%d nodes above the high watermark", n)
	}
	if v := verdict(t, st, index("i", 1, 1, hot(nil))); v.Status != OK {
		t.Errorf("verdict %+v", v)
	}
}

// require with several values: the attribute must match all of them (one
// value never matches two), except the IP attributes where one is enough.
func TestRequireAllValues(t *testing.T) {
	st := baseState()
	v := verdict(t, st, index("i", 1, 0, hot(map[string]string{"index.routing.allocation.require.dc": "dc1,dc2"})))
	if v.Status != Infeasible || v.Reason != ReasonNoEligibleNode {
		t.Errorf("require dc1,dc2: %+v", v)
	}
	st.ClusterSettings["cluster.routing.allocation.require.dc"] = "dc*,*1"
	if v := verdict(t, st, index("j", 1, 0, hot(nil))); v.Eligible != 2 {
		t.Errorf("cluster require dc*,*1: %+v", v)
	}
	st = baseState()
	st.Nodes[0].IP, st.Nodes[1].IP, st.Nodes[2].IP = "10.0.0.1", "10.0.0.2", "10.0.0.3"
	if v := verdict(t, st, index("k", 1, 0, hot(map[string]string{"index.routing.allocation.require._ip": "10.0.0.1,10.0.0.2"}))); v.Eligible != 2 {
		t.Errorf("require _ip: %+v", v)
	}
}

// require "*" needs the attribute: a node without it is not eligible
// (Elasticsearch 9.5: "node does not match index setting
// [index.routing.allocation.require] filters"), unlike include and exclude
// that skip it.
func TestRequireWildcardNeedsAttribute(t *testing.T) {
	st := baseState()
	delete(st.Nodes[0].Attrs, "dc")
	delete(st.Nodes[1].Attrs, "dc")
	if v := verdict(t, st, index("i", 1, 0, hot(map[string]string{"index.routing.allocation.require.dc": "*"}))); v.Eligible != 2 {
		t.Errorf("require dc *: %+v", v)
	}
	if v := verdict(t, st, index("j", 1, 0, hot(map[string]string{"index.routing.allocation.require._name": "h*"}))); v.Eligible != 4 {
		t.Errorf("require _name h*: %+v", v)
	}
}
