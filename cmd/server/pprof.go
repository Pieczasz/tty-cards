//go:build pprof

package main

// A profiling build (go build -tags pprof ./cmd/server) serves net/http/pprof on
// loopback, for profiling under cmd/loadtest. The production image never builds it.

import (
	"log/slog"
	"net/http"
	_ "net/http/pprof" //nolint:gosec // G108: loopback only, and only in a -tags pprof build
	"time"
)

func init() {
	go func() {
		srv := &http.Server{Addr: "127.0.0.1:6060", ReadHeaderTimeout: 5 * time.Second}
		slog.Warn("pprof listening", "address", srv.Addr)
		if err := srv.ListenAndServe(); err != nil {
			slog.Error("pprof stopped", "error", err)
		}
	}()
}
