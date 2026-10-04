package derive

import "testing"

func TestDetectFamily(t *testing.T) {
	for name, want := range map[string]string{
		"logs-app-2026.09.28":                 "logs-app",
		"logs-app-2026-09-28":                 "logs-app",
		"applog_20260928":                     "applog",
		"weblog-000002":                       "weblog",
		"archive-2026.08":                     "archive",
		"metrics-2026.09.28-000003":           "metrics",
		".ds-ilm-history-7-2026.09.30-000001": ".ds-ilm-history-7",
		"yearly-2025":                         "yearly",
		"weekly-2026.w40":                     "weekly",
		"plain":                               "plain",
		".security-7":                         ".security-7",
		"2026.09.28":                          "2026.09.28",
		"logs-042-2026.09.28":                 "logs-042",
		"app-v2":                              "app-v2",
		"sensor-12345":                        "sensor-12345",
		"metrics-8080":                        "metrics-8080", // four digits that are not a year
		"api-9200":                            "api-9200",
		"archive-1999":                        "archive",
		".monitoring-es-7-2026.09.30":         ".monitoring-es-7",
	} {
		if got := DetectFamily(name); got != want {
			t.Errorf("DetectFamily(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestFamilyRules(t *testing.T) {
	r1, _ := NewFamilyRule(`^(?P<family>tenant-[a-z]+)-`)
	r2, _ := NewFamilyRule(`^(audit)`)
	rules := []FamilyRule{r1, r2}
	for name, want := range map[string]string{
		"tenant-acme-logs-2026.09.28": "tenant-acme",
		"audit-trail-000001":          "audit",
		"other-2026.09.28":            "other",
	} {
		if got := Family(name, rules); got != want {
			t.Errorf("Family(%q) = %q, want %q", name, got, want)
		}
	}
	if _, err := NewFamilyRule("("); err == nil {
		t.Error("invalid regex accepted")
	}
}
