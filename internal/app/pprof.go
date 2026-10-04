package app

import (
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"
)

// startPprof serves the Go profiler on a loopback address (ESTOPO_PPROF_LISTEN),
// for diagnostics only; the address is validated by config.Load.
func startPprof(addr string, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info("profiler listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil {
			log.Error("profiler", "err", err)
		}
	}()
}
