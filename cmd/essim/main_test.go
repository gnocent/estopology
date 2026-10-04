package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestRunServesSmallCluster(t *testing.T) {
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"-clusters", "small", "-insecure", "-port", fmt.Sprint(port), "-version", "9.5.4"}, &out, io.Discard)
	}()
	url := fmt.Sprintf("http://127.0.0.1:%d/_cluster/health", port)
	var resp *http.Response
	var err error
	for i := 0; i < 100; i++ {
		if resp, err = http.Get(url); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"cluster_name":"small"`) {
		t.Errorf("health: %d %s", resp.StatusCode, body)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("exit code %d", code)
	}
	if !strings.Contains(out.String(), "small\thttp://") {
		t.Errorf("manifest: %q", out.String())
	}
}

func TestUnknownPreset(t *testing.T) {
	if code := run(context.Background(), []string{"-clusters", "zz", "-insecure"}, io.Discard, io.Discard); code != 2 {
		t.Errorf("exit code %d", code)
	}
}

// essim action drives a running cluster; the ES API sees the change.
func TestActionCommand(t *testing.T) {
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"-clusters", "small", "-insecure", "-port", fmt.Sprint(port)}, io.Discard, io.Discard)
	}()
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	var topo bytes.Buffer
	for i := 0; i < 100; i++ {
		topo.Reset()
		if run(ctx, []string{"action", "-url", url, "topology"}, &topo, io.Discard) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(topo.String(), "hot-dc1-m01") || !strings.Contains(topo.String(), "families: logs-") {
		t.Fatalf("topology: %s", topo.String())
	}
	var out, errOut bytes.Buffer
	if code := run(ctx, []string{"action", "-url", url, "node-leave", "node=hot-dc1-m01-1", "for=1h"}, &out, &errOut); code != 0 {
		t.Fatalf("node-leave: %d %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "small-hot-dc1-m01-1 for 1h0m0s") {
		t.Errorf("output: %s", out.String())
	}
	resp, err := http.Get(url + "/_cluster/health")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"status":"yellow"`) {
		t.Errorf("health after the node left: %s", body)
	}
	if code := run(ctx, []string{"action", "-url", url, "node-leave", "node=nope"}, io.Discard, io.Discard); code != 1 {
		t.Errorf("unknown node: %d", code)
	}
	if code := run(ctx, []string{"action", "-url", url, "fault", "status=abc"}, io.Discard, io.Discard); code != 2 {
		t.Errorf("bad status: %d", code)
	}
	cancel()
	<-done
}

// essim backfill builds a data directory ready for estopo serve.
func TestBackfillCommand(t *testing.T) {
	dir := t.TempDir() + "/demo"
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"backfill", "-data", dir, "-days", "1", "-clusters", "small"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("backfill: %d %s", code, errOut.String())
	}
	for _, want := range []string{"history ready", "ESTOPO_DATA_DIR=" + dir, "node-leave small-warm-dc1-m01-1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if code := run(context.Background(), []string{"backfill", "-data", dir, "-days", "1", "-clusters", "small"}, io.Discard, io.Discard); code != 2 {
		t.Errorf("existing directory replaced without -force: %d", code)
	}
}
