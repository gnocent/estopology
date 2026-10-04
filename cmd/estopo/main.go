// Command estopo collects the topology history of Elasticsearch clusters and
// serves the web UI to explore it (see SPEC_fr.md).
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"estopo/internal/app"
	"estopo/internal/auth"
	"estopo/internal/buildinfo"
	"estopo/internal/clock"
	"estopo/internal/config"
	"estopo/internal/logging"
	"estopo/internal/store"
	"estopo/web"
)

const usage = `Usage:
  estopo serve                        start the collector and the web UI
  estopo version                      print the version
  estopo admin reset-password <user>  reset a local user's password

Settings are read from ESTOPO_* environment variables (see README).
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, buildinfo.String("estopo"))
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	case "serve":
		cfg, err := config.Load(getenv, config.ExeDir())
		if err != nil {
			fmt.Fprintf(stderr, "estopo: invalid configuration:\n%v\n", err)
			return 1
		}
		log := logging.New(stdout, cfg.LogLevel, cfg.LogFormat)
		if err := app.Serve(ctx, cfg, log, web.Handler()); err != nil {
			log.Error("fatal", "err", err)
			return 1
		}
		return 0
	case "admin":
		if len(args) == 3 && args[1] == "reset-password" {
			return resetPassword(ctx, getenv, args[2], stdout, stderr)
		}
	}
	fmt.Fprint(stderr, usage)
	return 2
}

// resetPassword sets a new random password for a local account and prints
// it once (SPEC §10: recovery).
func resetPassword(ctx context.Context, getenv func(string) string, name string, stdout, stderr io.Writer) int {
	cfg, err := config.Load(getenv, config.ExeDir())
	if err != nil {
		fmt.Fprintf(stderr, "estopo: invalid configuration:\n%v\n", err)
		return 1
	}
	st, err := store.Open(ctx, cfg.DataDir, clock.Real{})
	if err != nil {
		fmt.Fprintf(stderr, "estopo: %v\n", err)
		return 1
	}
	defer st.Close()
	pw, err := auth.ResetPassword(ctx, st, name)
	if err != nil {
		fmt.Fprintf(stderr, "estopo: reset-password %s: %v\n", name, err)
		return 1
	}
	fmt.Fprintf(stdout, "New password for %s: %s\n", name, pw)
	return 0
}
