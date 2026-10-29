// Command amphora is the Go implementation of the Amphora secret share service.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/carbynestack/amphora-go/internal/castor"
	"github.com/carbynestack/amphora-go/internal/config"
	"github.com/carbynestack/amphora-go/internal/httpapi"
	"github.com/carbynestack/amphora-go/internal/jwt"
	"github.com/carbynestack/amphora-go/internal/objstore"
	"github.com/carbynestack/amphora-go/internal/opa"
	"github.com/carbynestack/amphora-go/internal/peer"
	"github.com/carbynestack/amphora-go/internal/pgstore"
	"github.com/carbynestack/amphora-go/internal/rediscache"
	"github.com/carbynestack/amphora-go/internal/service"
	"github.com/carbynestack/amphora-go/internal/spdz"
	"github.com/redis/go-redis/v9"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "0.3.0"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		slog.Error("amphora terminated", "err", err)
		os.Exit(1)
	}
}

// healthcheck is used by the container HEALTHCHECK (the image has no curl).
func healthcheck() int {
	port := os.Getenv("AMPHORA_SERVICE_PORT")
	if port == "" {
		port = "10000"
	}
	c := http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/actuator/health")
	if err != nil || resp.StatusCode != 200 {
		return 1
	}
	return 0
}

func logger(level string) *slog.Logger {
	lv := slog.LevelInfo
	switch level {
	case "DEBUG", "TRACE":
		lv = slog.LevelDebug
	case "WARN":
		lv = slog.LevelWarn
	case "ERROR":
		lv = slog.LevelError
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	log := logger(cfg.LogLevel)
	slog.SetDefault(log)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pg, err := pgstore.Open(ctx, pgstore.DSN(cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPassword, cfg.DBName))
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pg.Close()

	rdb := redis.NewClient(&redis.Options{Addr: net.JoinHostPort(cfg.RedisHost, fmt.Sprint(cfg.RedisPort))})
	defer rdb.Close()
	cache := rediscache.New(rdb, cfg.InputMaskStore, cfg.InterimValueStore)

	obj, err := objstore.New(ctx, cfg.MinioEndpoint, cfg.MinioBucket, cfg.MinioAccessKey, cfg.MinioSecretKey)
	if err != nil {
		return fmt.Errorf("Failed initializing MinoSecretShareDataStore.: %w", err)
	}
	opaClient, err := opa.New(cfg.OpaURL, cfg.OpaPolicyPackage, log)
	if err != nil {
		return err
	}
	cc, err := castor.New(cfg.CastorServiceURI, cfg.NoSSLValidation, cfg.TrustedCertificates)
	if err != nil {
		return err
	}
	var peers service.Peers
	if len(cfg.VCPartners) > 0 {
		pc, err := peer.New(cfg.VCPartners, cfg.NoSSLValidation, cfg.TrustedCertificates, 2*time.Minute)
		if err != nil {
			return err
		}
		peers = pc
	} else {
		// Java refuses to start without a partner; Go tolerates a single-player deployment.
		log.Warn("AMPHORA_VC_PARTNERS is empty; running as single player")
	}
	sp := spdz.New(cfg.Prime, cfg.R, cfg.RInv)
	odo := &service.ODO{Spdz: sp, Castor: cc, Peers: peers, Interim: cache, PlayerID: cfg.PlayerID,
		NumPartners: len(cfg.VCPartners), OpeningTimeout: time.Duration(cfg.OpeningTimeoutMs) * time.Millisecond, Log: log}
	svc := &service.Service{PG: pg, Obj: obj, Cache: cache, Opa: opaClient, Castor: cc, ODO: odo, Spdz: sp,
		MacKey: cfg.MacKey, PlayerID: cfg.PlayerID, JavaTagReplaceBug: cfg.JavaTagReplaceBug, Log: log}

	api := &httpapi.API{Svc: svc, JWT: jwt.Reader{Field: cfg.UserIDField},
		Checks: []httpapi.HealthCheck{pg.Ping, cache.Ping},
		Info: httpapi.Info{"name": "\"Amphora Service\"", "description": "\"Amphora bucket/object store service.\"", "groupId": "\"io.carbynestack\"",
			"artifactId": "\"amphora-service\"", "version": "\"" + version + "\"", "apiVersion": "\"" + version + "\""},
		Log: log}
	srv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.Port), Handler: api.Handler(), ReadHeaderTimeout: 30 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("amphora listening", "port", cfg.Port, "playerId", cfg.PlayerID, "partners", len(cfg.VCPartners))
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		sc, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sc)
	}
	return nil
}
