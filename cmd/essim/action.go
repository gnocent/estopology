package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"estopo/internal/sim"
)

// runAction drives a running essim cluster through its control API:
//
//	essim action [-url http://127.0.0.1:19200] topology
//	essim action [-url …] node-leave node=hot-dc1-m01-2 for=30m
func runAction(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("essim action", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", "http://127.0.0.1:19200", "URL of the simulated cluster (one port per cluster)")
	pkiDir := fs.String("pki", "essim-pki", "test PKI directory (https)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: essim action [-url URL] topology | <action> [key=value ...]")
		fmt.Fprintln(stderr, "\nActions (durations in simulated time; for=… undoes the action afterwards):")
		names := make([]string, 0, len(sim.Actions))
		for n := range sim.Actions {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(stderr, "  %-16s %s\n", n, sim.Actions[n])
		}
		fmt.Fprintln(stderr, "\nTargets are name suffixes: node=hot-dc1-m01-2, machine=warm-dc2-m02, dc=dc2; index=<name or family>.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return 2
	}
	client := &http.Client{Timeout: 30 * time.Second}
	base := strings.TrimRight(*url, "/")
	if strings.HasPrefix(base, "https://") {
		pem, err := os.ReadFile(filepath.Join(*pkiDir, "root.crt"))
		if err != nil {
			fmt.Fprintf(stderr, "essim: test CA: %v\n", err)
			return 1
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(pem)
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	}

	if fs.Arg(0) == "topology" {
		resp, err := client.Get(base + "/_essim/topology")
		if err != nil {
			fmt.Fprintf(stderr, "essim: %v\n", err)
			return 1
		}
		defer resp.Body.Close()
		var t sim.Topology
		if err := json.NewDecoder(resp.Body).Decode(&t); err != nil || resp.StatusCode != 200 {
			fmt.Fprintf(stderr, "essim: topology: %s %v\n", resp.Status, err)
			return 1
		}
		printTopology(stdout, t)
		return 0
	}

	body := map[string]any{"do": fs.Arg(0)}
	for _, kv := range fs.Args()[1:] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			fmt.Fprintf(stderr, "essim: %q is not key=value\n", kv)
			return 2
		}
		switch k {
		case "status":
			n, err := strconv.Atoi(v)
			if err != nil {
				fmt.Fprintf(stderr, "essim: status=%q\n", v)
				return 2
			}
			body[k] = n
		case "transient", "connected":
			b, err := strconv.ParseBool(v)
			if err != nil {
				fmt.Fprintf(stderr, "essim: %s=%q\n", k, v)
				return 2
			}
			body[k] = b
		default:
			body[k] = v
		}
	}
	b, _ := json.Marshal(body)
	resp, err := client.Post(base+"/_essim/action", "application/json", bytes.NewReader(b))
	if err != nil {
		fmt.Fprintf(stderr, "essim: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	var out map[string]string
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 {
		fmt.Fprintf(stderr, "essim: %s: %s\n", resp.Status, out["error"])
		return 1
	}
	fmt.Fprintf(stdout, "%s at %s (simulated): %s\n", out["applied"], out["at"], out["detail"])
	return 0
}

// printTopology lists the machines and their nodes (✗ = down), to pick
// targets.
func printTopology(w io.Writer, t sim.Topology) {
	fmt.Fprintf(w, "%s  version %s  simulated time %s\n", t.Cluster, t.Version, t.Now.Format(time.RFC3339))
	fmt.Fprintf(w, "data centres: %s   remotes: %s\n", strings.Join(t.DCs, ", "), strings.Join(t.Remotes, ", "))
	byMachine := map[string][]sim.TopologyNode{}
	var machines []string
	for _, n := range t.Nodes {
		if _, ok := byMachine[n.Machine]; !ok {
			machines = append(machines, n.Machine)
		}
		byMachine[n.Machine] = append(byMachine[n.Machine], n)
	}
	sort.Strings(machines)
	for _, m := range machines {
		var parts []string
		for _, n := range byMachine[m] {
			mark := ""
			if !n.Up {
				mark = " ✗"
			}
			ver := ""
			if n.Version != t.Version {
				ver = " (" + n.Version + ")"
			}
			parts = append(parts, n.Short+mark+ver)
		}
		fmt.Fprintf(w, "  %-28s %s\n", strings.TrimPrefix(m, t.Cluster+"-"), strings.Join(parts, "  "))
	}
	fams := t.Families
	if len(fams) > 12 {
		fams = append(fams[:12:12], fmt.Sprintf("… (%d)", len(t.Families)))
	}
	fmt.Fprintf(w, "families: %s\n", strings.Join(fams, ", "))
}
