// Command fileflow is the file gateway service.
//
// Phase 1 runs as a single process. The code is split so the two logical
// services (metadata/API and blob data path) can become separate deployments
// in phase 5 without rewriting handlers. See ROADMAP.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/luka/fileflow/internal/config"
	"github.com/luka/fileflow/internal/httpapi"
	"github.com/luka/fileflow/internal/obs"
	"github.com/luka/fileflow/internal/storage"
	"github.com/luka/fileflow/internal/storage/azure"
	"github.com/luka/fileflow/internal/storage/local"
	"github.com/luka/fileflow/internal/storage/s3"
)

var version = "dev" // set with -ldflags "-X main.version=$(git describe)"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := obs.NewLogger(os.Stdout, cfg.LogLevel, cfg.LogFormat, "fileflow", version)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	backend, err := newBackend(ctx, cfg)
	if err != nil {
		return err
	}
	api := httpapi.New(backend, log, httpapi.Options{MaxUploadBytes: cfg.MaxUploadBytes})

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: obs.Middleware(obs.MiddlewareConfig{Logger: log, IdleIOTimeout: cfg.IdleIOTimeout}, api.Routes()),
		// No Read/WriteTimeout: they would kill long transfers. Slow or
		// stalled clients are handled by the middleware's idle-progress
		// deadlines instead.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	if cfg.EnableH2C {
		// HTTP/2 without TLS (Go 1.24+): useful between an ingress/Front Door
		// that terminates TLS and the pods; multiplexes many small requests
		// over one connection.
		var p http.Protocols
		p.SetHTTP1(true)
		p.SetUnencryptedHTTP2(true)
		srv.Protocols = &p
	}

	admin := &http.Server{Addr: cfg.AdminAddr, Handler: adminMux(), ReadHeaderTimeout: 5 * time.Second}

	errc := make(chan error, 2)
	go func() { errc <- serve(srv) }()
	go func() { errc <- serve(admin) }()
	log.Info("fileflow started", "addr", cfg.ListenAddr, "admin", cfg.AdminAddr, "backend", backend.Name(),
		"part_size", cfg.PartSize, "part_concurrency", cfg.PartConcurrency, "h2c", cfg.EnableH2C)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	// Graceful shutdown: fail readiness first so Kubernetes / the LB stops
	// sending new requests, then let in-flight transfers finish.
	log.Info("shutdown: draining", "drain_delay", cfg.DrainDelay)
	api.SetReady(false)
	time.Sleep(cfg.DrainDelay)
	sctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	err = srv.Shutdown(sctx)
	_ = admin.Shutdown(sctx)
	log.Info("shutdown complete", "err", errString(err))
	return nil
}

func newBackend(ctx context.Context, cfg config.Config) (storage.Backend, error) {
	switch cfg.Backend {
	case "s3":
		return s3.New(ctx, s3.Config{
			Endpoint: cfg.S3Endpoint, Region: cfg.S3Region, Bucket: cfg.S3Bucket,
			AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey, UsePathStyle: cfg.S3PathStyle,
			PartSize: cfg.PartSize, Concurrency: cfg.PartConcurrency, CreateBucket: cfg.S3CreateBucket,
		})
	case "azure":
		return azure.New(ctx, azure.Config{
			ConnectionString: cfg.AzureConnString, AccountURL: cfg.AzureAccountURL,
			Container: cfg.AzureContainer, BlockSize: cfg.PartSize, Concurrency: cfg.PartConcurrency,
			CreateContainer: cfg.AzureCreateContainer,
		})
	default:
		return local.New(cfg.LocalRoot, cfg.LocalFsync)
	}
}

// adminMux serves operator endpoints on a separate port that is only
// reachable inside the cluster: Prometheus metrics and Go pprof profiles.
func adminMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		obs.Default.WritePrometheus(w)
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		fmt.Fprintf(w, "# TYPE go_goroutines gauge\ngo_goroutines %d\n", runtime.NumGoroutine())
		fmt.Fprintf(w, "# TYPE go_memstats_heap_inuse_bytes gauge\ngo_memstats_heap_inuse_bytes %d\n", ms.HeapInuse)
		fmt.Fprintf(w, "# TYPE go_gc_cycles_total counter\ngo_gc_cycles_total %d\n", ms.NumGC)
	})
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

func serve(s *http.Server) error {
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("listen %s: %w", s.Addr, err)
	}
	return nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
