// Command router is the entry point for the model-router proxy.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelrouter/router/internal/auth"
	"github.com/modelrouter/router/internal/balancer"
	"github.com/modelrouter/router/internal/config"
	"github.com/modelrouter/router/internal/handler"
	"github.com/modelrouter/router/internal/health"
	"github.com/modelrouter/router/internal/metrics"
	"github.com/modelrouter/router/internal/proxy"
	"github.com/modelrouter/router/internal/service"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

type flags struct {
	port       int
	host       string
	logLevel   string
	configPath string
}

func parseFlags() flags {
	var f flags
	flag.IntVar(&f.port, "port", 8080, "TCP port to listen on")
	flag.StringVar(&f.host, "host", "0.0.0.0", "address to listen on")
	flag.StringVar(&f.logLevel, "log-level", "info", "log level: debug|info|warn|error")
	flag.StringVar(&f.configPath, "config", "./config.yaml", "path to YAML config")
	flag.Parse()
	return f
}

func run() error {
	f := parseFlags()

	level, err := parseLevel(f.logLevel)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	cfg, err := config.Load(f.configPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	m := metrics.New()

	// Backend monitor: detect engines, health-check, optionally poll queues.
	models := make(map[string][]string, len(cfg.Models))
	for name, mdl := range cfg.Models {
		models[name] = mdl.URLs
	}
	mon := health.NewMonitor(models, health.Options{
		HealthInterval: cfg.HealthRetry(),
		QueueInterval:  cfg.QueuePoll(),
		PollQueue:      cfg.Global.LoadBalancing == config.LBLessQueue,
		Logger:         log,
		Metrics:        m,
	})
	mon.Start(ctx)

	bal := balancer.New(cfg.Global.LoadBalancing)
	svc := service.New(cfg, mon, bal)
	prx := proxy.New(log)
	a := auth.New(cfg)
	h := handler.New(svc, prx, a, m, log)

	logStartupSummary(log, cfg, f, svc)

	addr := net.JoinHostPort(f.host, fmt.Sprintf("%d", f.port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           h.Routes(),
		ReadHeaderTimeout: 15 * time.Second,
		// No global write timeout: streaming responses may run long; the
		// per-request proxy timeout governs upstream duration instead.
	}

	tlsConf, err := buildTLSConfig(cfg.TLS)
	if err != nil {
		return err
	}
	srv.TLSConfig = tlsConf

	errCh := make(chan error, 1)
	go func() {
		var serveErr error
		if tlsConf != nil {
			serveErr = srv.ListenAndServeTLS("", "")
		} else {
			serveErr = srv.ListenAndServe()
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received, draining active requests")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
		return err
	}
	log.Info("shutdown complete")
	return nil
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q: must be debug|info|warn|error", s)
	}
}

func buildTLSConfig(t config.TLS) (*tls.Config, error) {
	if t.Cert == "" || t.Key == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(t.Cert, t.Key)
	if err != nil {
		return nil, fmt.Errorf("load server keypair: %w", err)
	}
	conf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	if t.Verify {
		caPEM, err := os.ReadFile(t.CACert)
		if err != nil {
			return nil, fmt.Errorf("read cacert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("cacert %q contains no valid certificates", t.CACert)
		}
		conf.ClientCAs = pool
		conf.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return conf, nil
}

func logStartupSummary(log *slog.Logger, cfg *config.Config, f flags, svc *service.Service) {
	mtls := cfg.TLS.Verify && cfg.TLS.Cert != ""
	tlsEnabled := cfg.TLS.Cert != ""
	log.Info("starting model-router",
		"addr", net.JoinHostPort(f.host, fmt.Sprintf("%d", f.port)),
		"tls", tlsEnabled, "mtls", mtls,
		"loadBalancing", string(cfg.Global.LoadBalancing),
		"requestTimeoutSecs", cfg.Global.RequestTimeoutSecs,
		"healthRetrySec", cfg.Global.HealthRetrySec,
		"queuePollSec", cfg.Global.QueuePollSec,
	)
	for _, name := range svc.Monitor().Models() {
		healthy, _ := svc.Monitor().Healthy(name)
		mdl := cfg.Models[name]
		log.Info("model configured",
			"model", name,
			"backends", len(mdl.URLs),
			"healthy", len(healthy),
			"requestTimeoutSecs", int(cfg.RequestTimeout(name).Seconds()),
		)
	}
	for name, cl := range cfg.Clients {
		limits := make([]string, 0, len(cl.ConcurrencyLimit))
		for _, l := range cl.ConcurrencyLimit {
			limits = append(limits, fmt.Sprintf("%s=%d", l.Model, l.MaxParallel))
		}
		desc := "unlimited"
		if len(limits) > 0 {
			desc = strings.Join(limits, ",")
		}
		log.Info("client configured", "client", name, "cn", cl.CN, "limits", desc)
	}
}
