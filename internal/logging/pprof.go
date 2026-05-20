package logging

import (
	"log/slog"
	"net/http"
	_ "net/http/pprof" // #nosec G108 -- handlers gated by PprofEnabled (default off) and only bound to localhost:6060
	"time"
)

// startPprof starts a pprof HTTP server on localhost:6060.
// Only called when PprofEnabled is true in config (default: off).
//
// The server uses explicit timeouts (G114) so a slow/idle client cannot wedge
// the goroutine forever. WriteTimeout is generous because /debug/pprof/profile
// holds the connection open for the full sampling duration (default 30s).
func startPprof() {
	go func() {
		addr := "localhost:6060"
		srv := &http.Server{
			Addr:              addr,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      120 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		Logger().Info("pprof_server_start", slog.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil {
			Logger().Error("pprof_server_error", slog.String("error", err.Error()))
		}
	}()
}
