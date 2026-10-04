package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"estopo/internal/backfill"
	"estopo/internal/sim"
)

// defaultVersions gives the clusters different versions, as in a real fleet.
var defaultVersions = map[string]string{"a": "7.17.29", "b": "8.19.22", "c": "9.5.4", "small": "8.19.22"}

// runBackfill builds an estopo data directory holding days of history that
// end now, with the incidents of a scenario: estopo serve can then explore
// it as after a real incident.
func runBackfill(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("essim backfill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	data := fs.String("data", "", "estopo data directory to create (required)")
	days := fs.Int("days", 14, "days of history")
	clusters := fs.String("clusters", "b,c,small", "comma-separated presets: a (245k shards, slow), b, c, small")
	versions := fs.String("versions", "", "per-cluster versions (default a=7.17.29,b=8.19.22,c=9.5.4,small=8.19.22)")
	scenario := fs.String("scenario", "demo", `incident scenario: "demo", a JSON file, or "none"`)
	endStr := fs.String("end", "", "end of the history (RFC 3339, default now)")
	force := fs.Bool("force", false, "replace the data directory if it exists")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: essim backfill -data DIR [-days 14] [-clusters b,c,small] [-scenario demo|file.json|none]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *data == "" || *days <= 0 {
		fs.Usage()
		return 2
	}
	dir, err := filepath.Abs(*data)
	if err != nil {
		fmt.Fprintf(stderr, "essim: %v\n", err)
		return 2
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		if !*force {
			fmt.Fprintf(stderr, "essim: %s is not empty (use -force to replace it)\n", dir)
			return 2
		}
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(stderr, "essim: %v\n", err)
			return 1
		}
	}

	end := time.Now().UTC().Truncate(time.Minute)
	if *endStr != "" {
		t, err := time.Parse(time.RFC3339, *endStr)
		if err != nil {
			fmt.Fprintf(stderr, "essim: -end: %v\n", err)
			return 2
		}
		end = t.UTC()
	}
	start := end.Add(-time.Duration(*days) * 24 * time.Hour)

	perVersion := map[string]string{}
	for k, v := range defaultVersions {
		perVersion[k] = v
	}
	for _, kv := range strings.Split(*versions, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			perVersion[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	var cls []backfill.Cluster
	for _, name := range strings.Split(*clusters, ",") {
		name = strings.TrimSpace(name)
		preset, ok := presets[name]
		if !ok {
			fmt.Fprintf(stderr, "essim: unknown preset %q\n", name)
			return 2
		}
		cls = append(cls, backfill.Cluster{ID: name, Spec: preset(perVersion[name])})
	}
	var sc *sim.Scenario
	if *scenario != "" && *scenario != "none" {
		if sc, err = sim.LoadScenario(*scenario); err != nil {
			fmt.Fprintf(stderr, "essim: %v\n", err)
			return 2
		}
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		fmt.Fprintf(stderr, "essim: %v\n", err)
		return 1
	}
	logPath := dir + ".log"
	logf, err := os.Create(logPath)
	if err != nil {
		fmt.Fprintf(stderr, "essim: %v\n", err)
		return 1
	}
	defer logf.Close()
	fmt.Fprintf(stdout, "backfill of %d days (%s → %s) of %s into %s (estopo logs: %s)\n",
		*days, start.Format(time.RFC3339), end.Format(time.RFC3339), *clusters, dir, logPath)
	res, err := backfill.Run(ctx, backfill.Options{
		Dir: dir, Start: start, Days: *days, Clusters: cls, Scenario: sc, DisableAtEnd: true,
		Log:    slog.New(slog.NewTextHandler(logf, nil)),
		OnStep: func(msg string) { fmt.Fprintln(stdout, "  "+msg) },
		Progress: func(day string, dayWall, total time.Duration, _ uint64) {
			fmt.Fprintf(stdout, "%s done in %v (total %v)\n", day, dayWall.Round(time.Second), total.Round(time.Second))
		},
	})
	if err != nil {
		fmt.Fprintf(stderr, "essim: backfill: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "\nhistory ready in %v. The clusters are disabled (their simulators are gone). Explore it with:\n\n", res.Wall.Round(time.Second))
	fmt.Fprintf(stdout, "  ESTOPO_LISTEN=127.0.0.1:8080 ESTOPO_DATA_DIR=%s estopo serve\n\nthen open http://127.0.0.1:8080\n", dir)
	return 0
}
