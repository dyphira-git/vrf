package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/vexxvakan/vrf/sidecar"
	"github.com/vexxvakan/vrf/sidecar/servers/prometheus"
	sidecarmetrics "github.com/vexxvakan/vrf/sidecar/servers/prometheus/metrics"
	vrfserver "github.com/vexxvakan/vrf/sidecar/servers/vrf"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, showVersion, err := parseFlags(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 2
	}

	if showVersion {
		printVersion(os.Stdout)
		return 0
	}

	logger, err := zap.NewProduction()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = logger.Sync() }()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := validateBindConfig(cfg); err != nil {
		logger.Error(err.Error())
		return 1
	}

	if err := cfg.GRPC.Validate(); err != nil {
		logger.Error("invalid gRPC server config", zap.Error(err))
		return 1
	}

	metrics, err := sidecarmetrics.NewFromConfig(cfg.MetricsEnabled, cfg.ChainID)
	if err != nil {
		logger.Error("failed to initialize metrics", zap.Error(err))
		return 1
	}

	if cfg.MetricsEnabled {
		ps, err := prometheus.NewPrometheusServer(cfg.MetricsAddr, logger)
		if err != nil {
			logger.Error("failed to create prometheus server", zap.Error(err))
			return 1
		}
		go ps.Start()
		defer ps.Close()
	}

	if cfg.ChainWSEnabled && strings.TrimSpace(cfg.ChainRPCAddr) == "" {
		logger.Error("--chain-rpc-addr is required when --chain-ws-enabled=true")
		return 1
	}

	versionMode, err := sidecar.ParseDrandVersionCheckMode(cfg.DrandVersionCheck)
	if err != nil {
		logger.Error("invalid --drand-version-check value", zap.Error(err))
		return 1
	}

	drandCfg := sidecar.Config{
		DrandSupervise:            cfg.DrandSupervise,
		DrandHTTP:                 strings.TrimSpace(cfg.DrandHTTP),
		DrandAllowNonLoopbackHTTP: cfg.DrandAllowNonLoop,
		BinaryPath:                cfg.DrandBinary,
		DrandVersionCheck:         versionMode,
		DrandDataDir:              cfg.DrandDataDir,
		DrandPublicListen:         cfg.DrandPublicAddr,
		DrandPrivateListen:        cfg.DrandPrivateAddr,
		DrandControlListen:        cfg.DrandControlAddr,
	}

	if drandCfg.DrandHTTP == "" {
		drandCfg.DrandHTTP = "http://" + drandCfg.DrandPublicListen
	}

	if cfg.DrandChainHashHex != "" {
		chainHash, decodeErr := hex.DecodeString(cfg.DrandChainHashHex)
		if decodeErr != nil {
			logger.Error("invalid drand chain hash; must be hex", zap.Error(decodeErr))
			return 1
		}
		drandCfg.ChainHash = chainHash
	}

	if cfg.DrandPublicKeyB64 != "" {
		pubKey, decodeErr := base64.StdEncoding.DecodeString(cfg.DrandPublicKeyB64)
		if decodeErr != nil {
			logger.Error("invalid drand public key; must be base64", zap.Error(decodeErr))
			return 1
		}
		drandCfg.PublicKey = pubKey
	}

	if cfg.DrandPeriodSeconds > 0 {
		drandCfg.PeriodSeconds = uint64(cfg.DrandPeriodSeconds)
	}

	if cfg.DrandGenesisUnix > 0 {
		drandCfg.GenesisUnixSec = cfg.DrandGenesisUnix
	}

	dyn := sidecar.NewDynamicService(nil)

	if strings.TrimSpace(cfg.ChainGRPCAddr) != "" {
		var drandCtl *drandController
		if drandCfg.DrandSupervise {
			if strings.TrimSpace(drandCfg.DrandDataDir) == "" {
				logger.Error("--drand-data-dir is required when --drand-supervise=true")
				return 1
			}

			drandCtl = newDrandController(sidecar.DrandProcessConfig{
				BinaryPath:        drandCfg.BinaryPath,
				DataDir:           drandCfg.DrandDataDir,
				PrivateListen:     drandCfg.DrandPrivateListen,
				PublicListen:      drandCfg.DrandPublicListen,
				ControlListen:     drandCfg.DrandControlListen,
				DisableRestart:    cfg.DrandNoRestart,
				RestartBackoffMin: cfg.DrandRestartMin,
				RestartBackoffMax: cfg.DrandRestartMax,
			}, logger, metrics)
		}

		_, _, cleanup, err := startChainWatcher(ctx, cancel, logger, metrics, &drandCfg, cfg.ChainGRPCAddr, chainWatchConfig{
			pollInterval:     cfg.ChainPollInterval,
			wsEnabled:        cfg.ChainWSEnabled,
			cometRPCAddr:     cfg.ChainRPCAddr,
			reshareExtraArgs: cfg.DrandReshareArgs,
			reshareEnabled:   cfg.ReshareEnabled,
			reshareTimeout:   cfg.DrandReshareTimeout,
		}, dyn, drandCtl)
		if err != nil {
			logger.Error("failed to start chain watcher", zap.Error(err))
			return 1
		}
		defer cleanup()
	} else {
		if len(drandCfg.ChainHash) == 0 || len(drandCfg.PublicKey) == 0 || drandCfg.PeriodSeconds == 0 || drandCfg.GenesisUnixSec == 0 {
			logger.Error("drand chain configuration is incomplete; provide flags or enable --chain-grpc-addr watcher")
			return 1
		}

		dyn.SetInfo(infoFromConfig(drandCfg))

		var proc *sidecar.DrandProcess
		if drandCfg.DrandSupervise {
			if strings.TrimSpace(drandCfg.DrandDataDir) == "" {
				logger.Error("--drand-data-dir is required when --drand-supervise=true")
				return 1
			}

			proc, err = sidecar.StartDrandProcess(ctx, sidecar.DrandProcessConfig{
				BinaryPath:        drandCfg.BinaryPath,
				DataDir:           drandCfg.DrandDataDir,
				PrivateListen:     drandCfg.DrandPrivateListen,
				PublicListen:      drandCfg.DrandPublicListen,
				ControlListen:     drandCfg.DrandControlListen,
				DisableRestart:    cfg.DrandNoRestart,
				RestartBackoffMin: cfg.DrandRestartMin,
				RestartBackoffMax: cfg.DrandRestartMax,
			}, logger, metrics)
			if err != nil {
				logger.Error("failed to start drand subprocess", zap.Error(err))
				return 1
			}
			defer proc.Stop()
		}

		svc, err := newDrandServiceWithRetry(ctx, drandCfg, logger, metrics)
		if err != nil {
			logger.Error("failed to create drand service", zap.Error(err))
			return 1
		}
		dyn.SetService(svc)
	}

	server := vrfserver.NewServer(dyn, logger, metrics)
	if err := server.SetGRPCConfig(cfg.GRPC); err != nil {
		logger.Error("failed to configure gRPC server options", zap.Error(err))
		return 1
	}
	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		return server.Start(egCtx, cfg.ListenAddr)
	})
	if cfg.DebugHTTPEnabled {
		eg.Go(func() error {
			return server.StartDebugHTTP(egCtx, cfg.DebugHTTPAddr)
		})
	}

	if err := eg.Wait(); err != nil {
		logger.Error("sidecar server exited with error", zap.Error(err))
		return 1
	}

	return 0
}

func newDrandServiceWithRetry(
	ctx context.Context,
	cfg sidecar.Config,
	logger *zap.Logger,
	metrics sidecarmetrics.Metrics,
) (*sidecar.DrandService, error) {
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		svc, err := sidecar.NewDrandService(ctx, cfg, logger, metrics)
		if err == nil {
			return svc, nil
		}
		lastErr = err

		logger.Warn("drand service not ready yet; retrying", zap.Error(err))
		select {
		case <-time.After(1 * time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return nil, lastErr
}

func isLoopbackAddr(addr string) bool {
	if strings.HasPrefix(addr, "unix://") {
		return true
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}

	if strings.EqualFold(host, "localhost") {
		return true
	}

	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}

	return false
}

func printVersion(out io.Writer) {
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok || buildInfo == nil {
		_, _ = fmt.Fprintln(out, "sidecar")
		return
	}

	var (
		revision string
		modified string
	)
	for _, s := range buildInfo.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		default:
			continue
		}
	}

	version := strings.TrimSpace(buildInfo.Main.Version)
	if version == "" {
		version = "dev"
	}

	if revision == "" {
		_, _ = fmt.Fprintf(out, "sidecar %s\n", version)
		return
	}

	if modified == "" {
		modified = "unknown"
	}

	_, _ = fmt.Fprintf(out, "sidecar %s (%s, modified=%s)\n", version, revision, modified)
}
