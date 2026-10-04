package i18n

import (
	"strings"
	"testing"

	"estopo/internal/derive"
)

func TestCataloguesHaveTheSameKeys(t *testing.T) {
	fr, en := Keys(FR), Keys(EN)
	if strings.Join(fr, ",") != strings.Join(en, ",") {
		missing := func(a, b []string) []string {
			in := map[string]bool{}
			for _, k := range b {
				in[k] = true
			}
			var out []string
			for _, k := range a {
				if !in[k] {
					out = append(out, k)
				}
			}
			return out
		}
		t.Errorf("FR only: %v; EN only: %v", missing(fr, en), missing(en, fr))
	}
	for _, k := range fr {
		if T(FR, k) == "" || T(EN, k) == "" {
			t.Errorf("empty label %s", k)
		}
	}
}

// Every code the API can return has a label.
func TestCodesAreTranslated(t *testing.T) {
	codes := []string{
		"event.health_changed", "event.node_left", "event.node_joined", "event.node_new_generation", "event.node_upgraded",
		"event.node_attrs_changed", "event.machine_lost", "event.index_created", "event.index_deleted", "event.index_closed",
		"event.index_opened", "event.index_settings_changed", "event.cluster_settings_changed", "event.shard_relocated",
		"event.shard_promoted", "event.shard_lost", "event.shard_unassigned", "event.shard_assigned",
		"event.remote_disconnected", "event.remote_connected", "event.capture_failed", "event.cluster_uuid_changed",
	}
	for _, c := range []string{derive.ConcernUnassignedPrimaries, derive.ConcernMissingNodes, derive.ConcernUnassignedReplicas,
		derive.ConcernHighWatermark, derive.ConcernInfeasible, derive.ConcernMoving} {
		codes = append(codes, "concern."+c)
	}
	for _, c := range []string{derive.ReasonNoEligibleNode, derive.ReasonNotEnoughNodes, derive.ReasonSameHost, derive.ReasonTotalShards,
		derive.ReasonClusterTotal, derive.ReasonAwareness, derive.ReasonLoseNode, derive.ReasonLoseMachine} {
		codes = append(codes, "reason."+c)
	}
	for _, c := range codes {
		if T(FR, c) == c || T(EN, c) == c {
			t.Errorf("no label for %s", c)
		}
	}
	if Normalize("en-US") != EN || Normalize("") != FR || Normalize("fr_FR") != FR {
		t.Error("Normalize")
	}
}
