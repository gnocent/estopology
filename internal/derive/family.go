// Package derive holds the computations drawn from the stored history:
// families, severity, node reference, allocation constraints, losses
// (SPEC §6.5, §7).
package derive

import (
	"regexp"
	"strings"
)

// Suffixes stripped, repeatedly, from the end of an index name to find its
// family (SPEC §7.3): rollover counters and date stamps.
var familySuffixes = []*regexp.Regexp{
	regexp.MustCompile(`[-_.]\d{6}$`),                        // -000001 (rollover)
	regexp.MustCompile(`[-_.]?\d{4}[.\-_]\d{2}[.\-_]\d{2}$`), // yyyy.MM.dd, yyyy-MM-dd
	regexp.MustCompile(`[-_.]?\d{4}\d{2}\d{2}$`),             // yyyyMMdd
	regexp.MustCompile(`[-_.]\d{4}[.\-_]\d{2}$`),             // yyyy.MM, yyyy-MM
	regexp.MustCompile(`[-_.]\d{4}[.\-_]w\d{2}$`),            // yyyy.w40 (weekly)
	regexp.MustCompile(`[-_.](?:199\d|20\d{2})$`),            // yyyy (yearly, 1990-2099), only as a separate token: a port or an id is not a year
}

// DetectFamily returns the family of an index name by removing date and
// rollover suffixes ("logs-app-2026.09.28" → "logs-app",
// "weblog-000002" → "weblog"). A name without such suffix is its own family.
func DetectFamily(name string) string {
	f := name
	for changed := true; changed; {
		changed = false
		for _, re := range familySuffixes {
			if loc := re.FindStringIndex(f); loc != nil && loc[0] > 0 {
				f = f[:loc[0]]
				changed = true
			}
		}
	}
	f = strings.TrimRight(f, "-_.")
	if f == "" {
		return name
	}
	return f
}

// FamilyRule overrides the detection for a cluster: the first rule whose
// regex matches gives the family (named group "family", else the first
// group, else the whole match).
type FamilyRule struct {
	re *regexp.Regexp
}

// NewFamilyRule compiles a rule.
func NewFamilyRule(pattern string) (FamilyRule, error) {
	re, err := regexp.Compile(pattern)
	return FamilyRule{re: re}, err
}

// Family applies the rules, then the automatic detection.
func Family(name string, rules []FamilyRule) string {
	for _, r := range rules {
		m := r.re.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		if i := r.re.SubexpIndex("family"); i > 0 && m[i] != "" {
			return m[i]
		}
		if len(m) > 1 && m[1] != "" {
			return m[1]
		}
		return m[0]
	}
	return DetectFamily(name)
}
