// Command essim simulates Elasticsearch clusters (7.17, 8.x, 9.x APIs used
// by estopo) for tests, benchmarks and demos (SPEC §13.1).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"estopo/internal/buildinfo"
	"estopo/internal/clock"
	"estopo/internal/sim"
	"estopo/internal/testpki"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

var presets = map[string]func(version string) sim.Spec{
	"a": sim.PresetA, "b": sim.PresetB, "c": sim.PresetC, "small": sim.PresetSmall,
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version":
			fmt.Fprintln(stdout, buildinfo.String("essim"))
			return 0
		case "backfill":
			return runBackfill(ctx, args[1:], stdout, stderr)
		case "action":
			return runAction(args[1:], stdout, stderr)
		}
	}
	fs := flag.NewFlagSet("essim", flag.ContinueOnError)
	fs.SetOutput(stderr)
	clusters := fs.String("clusters", "small", "comma-separated presets: a, b, c, small")
	version := fs.String("version", "8.19.22", "Elasticsearch version (response format) of every cluster")
	versions := fs.String("versions", "", "per-cluster versions, e.g. a=7.17.29,b=9.5.4")
	listen := fs.String("listen", "127.0.0.1", "listen address")
	port := fs.Int("port", 19200, "port of the first cluster; the next ones follow")
	pkiDir := fs.String("pki", "essim-pki", "test PKI directory (generated if missing)")
	insecure := fs.Bool("insecure", false, "plain HTTP without authentication")
	basic := fs.String("basic", "estopo:estopo-test", "accepted basic credentials user:password (test value)")
	apiKey := fs.String("apikey", "", "accepted API key (encoded form)")
	startStr := fs.String("start", "", "simulated start time (RFC 3339, default now)")
	speed := fs.Float64("speed", 1, "simulated seconds per real second")
	scenario := fs.String("scenario", "", `incidents to play while serving: "demo" or a JSON file (steps counted from the start)`)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	var sc *sim.Scenario
	if *scenario != "" {
		var err error
		if sc, err = sim.LoadScenario(*scenario); err != nil {
			fmt.Fprintf(stderr, "essim: %v\n", err)
			return 2
		}
	}

	start := time.Now().UTC()
	if *startStr != "" {
		t, err := time.Parse(time.RFC3339, *startStr)
		if err != nil {
			fmt.Fprintf(stderr, "essim: -start: %v\n", err)
			return 2
		}
		start = t
	}
	perVersion := map[string]string{}
	for _, kv := range strings.Split(*versions, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			perVersion[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}

	auth := sim.Auth{Disabled: *insecure, Users: map[string]string{}, APIKeys: map[string]bool{}}
	if u, p, ok := strings.Cut(*basic, ":"); ok {
		auth.Users[u] = p
	}
	if *apiKey != "" {
		auth.APIKeys[*apiKey] = true
	}
	var pki *testpki.PKI
	if !*insecure {
		var err error
		pki, err = loadOrCreatePKI(*pkiDir)
		if err != nil {
			fmt.Fprintf(stderr, "essim: PKI: %v\n", err)
			return 1
		}
	}

	clk := clock.NewScaled(start, *speed)
	var servers []*http.Server
	errc := make(chan error, 8)
	for i, name := range strings.Split(*clusters, ",") {
		name = strings.TrimSpace(name)
		preset, ok := presets[name]
		if !ok {
			fmt.Fprintf(stderr, "essim: unknown preset %q\n", name)
			return 2
		}
		ver := *version
		if v, ok := perVersion[name]; ok {
			ver = v
		}
		t0 := time.Now()
		c := sim.Build(preset(ver), start)
		addr := net.JoinHostPort(*listen, strconv.Itoa(*port+i))
		simSrv := &sim.Server{Cluster: c, Clock: clk, Auth: auth}
		if sc != nil {
			id := name
			if err := simSrv.Play(sc, id, start, time.Time{}, func(f string, args ...any) { log.Info(fmt.Sprintf(f, args...)) }); err != nil {
				fmt.Fprintf(stderr, "essim: scenario: %v (in direct mode, steps count from the start)\n", err)
				return 2
			}
		}
		srv := &http.Server{Addr: addr, Handler: simSrv, ReadHeaderTimeout: 10 * time.Second}
		scheme := "http"
		if pki != nil {
			tc, err := sim.ServerTLS(pki)
			if err != nil {
				fmt.Fprintf(stderr, "essim: %v\n", err)
				return 1
			}
			srv.TLSConfig = tc
			scheme = "https"
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Fprintf(stderr, "essim: %v\n", err)
			return 1
		}
		servers = append(servers, srv)
		go func() {
			if pki != nil {
				errc <- srv.ServeTLS(ln, "", "")
			} else {
				errc <- srv.Serve(ln)
			}
		}()
		fmt.Fprintf(stdout, "%s\t%s://%s\tversion=%s\tnodes=%d\tindices=%d\tbuilt in %v\n",
			c.Name, scheme, addr, ver, len(c.Nodes), len(c.Indices), time.Since(t0).Round(time.Millisecond))
	}
	if pki != nil {
		fmt.Fprintf(stdout, "CA: %s  client cert: %s  key: %s\n", pki.RootCert, pki.ClientCert, pki.ClientKey)
	}
	log.Info("simulating", "start", start.Format(time.RFC3339), "speed", *speed)

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "essim: %v\n", err)
			return 1
		}
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		s.Shutdown(sctx)
	}
	return 0
}

// loadOrCreatePKI reuses an existing test PKI or generates one.
func loadOrCreatePKI(dir string) (*testpki.PKI, error) {
	p := &testpki.PKI{
		Dir: dir, RootCert: filepath.Join(dir, "root.crt"), CAChain: filepath.Join(dir, "ca-chain.crt"),
		ServerCert: filepath.Join(dir, "server.crt"), ServerKey: filepath.Join(dir, "server.key"),
		ClientCert: filepath.Join(dir, "client.crt"), ClientKey: filepath.Join(dir, "client.key"),
	}
	if _, err := os.Stat(p.ClientKey); err == nil {
		return p, nil
	}
	return testpki.Generate(dir)
}
