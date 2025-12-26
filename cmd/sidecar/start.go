package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	sidecarv1 "github.com/vexxvakan/vrf/api/vexxvakan/sidecar/v1"
	"github.com/vexxvakan/vrf/sidecar"
	"github.com/vexxvakan/vrf/sidecar/chainws"
	"github.com/vexxvakan/vrf/sidecar/drand"
	sidecarmetrics "github.com/vexxvakan/vrf/sidecar/servers/metrics"
	vrfserver "github.com/vexxvakan/vrf/sidecar/servers/vrf"
)

func runStart(parent context.Context, args []string) int {
	cfg, err := parseFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 2
	}

	zapCfg := zap.NewProductionConfig()
	zapCfg.DisableStacktrace = true
	logger, err := zapCfg.Build()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = logger.Sync() }()

	if parent == nil {
		parent = context.Background()
	}

	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := ensureStartPrereqs(cfg); err != nil {
		logger.Error("failed to prepare sidecar runtime", zap.Error(err))
		return 1
	}

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
		ps, err := sidecarmetrics.NewPrometheusServer(cfg.MetricsAddr, logger)
		if err != nil {
			logger.Error("failed to create prometheus server", zap.Error(err))
			return 1
		}
		go ps.Start()
		defer ps.Close()
	}

	var dkgMgr atomic.Pointer[dkgManager]
	var reshareEvents <-chan chainws.ReshareScheduledEvent
	var initialDKGEvents <-chan chainws.InitialDKGEvent
	var paramsUpdatedEvents <-chan chainws.ParamsUpdatedEvent

	ws, err := chainws.NewPipeline(ctx, cfg.ChainRPCAddr, logger)
	if err != nil {
		logger.Error("failed to start chain websocket listener", zap.Error(err))
		return 1
	}
	defer ws.Close()

	reshareCh := make(chan chainws.ReshareScheduledEvent, 32)
	initialDKGCh := make(chan chainws.InitialDKGEvent, 16)
	paramsUpdatedCh := make(chan chainws.ParamsUpdatedEvent, 32)
	reshareEvents = reshareCh
	initialDKGEvents = initialDKGCh
	paramsUpdatedEvents = paramsUpdatedCh

	ws.OnReshareScheduled(func(_ context.Context, info chainws.ReshareScheduledEvent) {
		select {
		case reshareCh <- info:
		default:
			logger.Warn(
				"dropping vrf reshare event (buffer full)",
				zap.Uint64("reshare_epoch", info.NewEpoch),
				zap.Int64("height", info.Height),
				zap.String("scheduler", info.Scheduler),
			)
		}
	})

	ws.OnInitialDKG(func(handlerCtx context.Context, info chainws.InitialDKGEvent) {
		select {
		case initialDKGCh <- info:
		default:
			logger.Warn(
				"dropping vrf initial dkg event (buffer full)",
				zap.Int64("height", info.Height),
				zap.String("initiator", info.Initiator),
			)
		}

		mgr := dkgMgr.Load()
		if mgr == nil {
			return
		}

		go func() {
			if err := mgr.RunInitial(handlerCtx, info); err != nil {
				logger.Error("initial DKG failed", zap.Error(err))
			}
		}()
	})

	ws.OnParamsUpdated(func(_ context.Context, info chainws.ParamsUpdatedEvent) {
		select {
		case paramsUpdatedCh <- info:
		default:
			logger.Warn(
				"dropping vrf params update event (buffer full)",
				zap.Int64("height", info.Height),
				zap.String("authority", info.Authority),
			)
		}
	})

	ws.OnEmergencyDisable(func(_ context.Context, info chainws.EmergencyDisableEvent) {
		logger.Warn(
			"observed VRF emergency disable message",
			zap.Int64("height", info.Height),
			zap.String("authority", info.Authority),
			zap.String("reason", info.Reason),
		)

		ctl := dkgMgr.Load()
		if ctl != nil && ctl.ctl != nil {
			ctl.ctl.Stop()
		}
	})

	versionMode := drand.DrandVersionCheckStrict

	drandCfg := drand.Config{
		DrandHTTP:                 strings.TrimSpace(cfg.DrandHTTP),
		DrandAllowNonLoopbackHTTP: cfg.DrandAllowNonLoop,
		BinaryPath:                cfg.DrandBinary,
		DrandVersionCheck:         versionMode,
		DrandDataDir:              cfg.DrandDataDir,
		DrandID:                   strings.TrimSpace(cfg.DrandID),
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
	dyn.SetInfo(infoFromConfig(drandCfg))

	go logDrandStatus(ctx, logger, dyn, drandCfg.DrandHTTP, 15*time.Second)

	var drandCtl *drandController
	if strings.TrimSpace(drandCfg.DrandDataDir) == "" {
		configPath, configPathSet := findFlagValue(args, "drand-config")
		if !configPathSet {
			configPath = defaultDrandConfigPath()
		}

		if strings.TrimSpace(configPath) != "" {
			if _, statErr := os.Stat(configPath); statErr != nil {
				if errors.Is(statErr, os.ErrNotExist) {
					if home, ok := inferChainHomeFromVrfPath(configPath); ok {
						logger.Error(
							fmt.Sprintf("vrf.toml not found; run `sidecar init %s`", home),
							zap.String("path", configPath),
						)
					} else {
						logger.Error("vrf.toml not found; run `sidecar init <chain_home>`", zap.String("path", configPath))
					}
					return 1
				}
				logger.Error("unable to stat vrf.toml", zap.String("path", configPath), zap.Error(statErr))
				return 1
			}

			logger.Error(
				"vrf.toml is missing required drand data_dir; set data_dir in vrf.toml",
				zap.String("config", configPath),
			)
			return 1
		}

		logger.Error("drand data_dir is required; set data_dir in vrf.toml")
		return 1
	}

	drandCtl = newDrandController(drand.DrandProcessConfig{
		BinaryPath:        drandCfg.BinaryPath,
		DataDir:           drandCfg.DrandDataDir,
		ID:                drandCfg.DrandID,
		PrivateListen:     drandCfg.DrandPrivateListen,
		PublicListen:      drandCfg.DrandPublicListen,
		ControlListen:     drandCfg.DrandControlListen,
		DisableRestart:    cfg.DrandNoRestart,
		RestartBackoffMin: cfg.DrandRestartMin,
		RestartBackoffMax: cfg.DrandRestartMax,
	}, logger, metrics)
	defer drandCtl.Stop()
	dkgCfg := buildDKGConfig(cfg)
	dkgMgr.Store(newDKGManager(dkgCfg, drandCfg, drandCtl, logger))
	if cfg.ChainTimeoutCommit > 0 {
		blockTimeout := cfg.ChainTimeoutCommit + chainBlockTimeoutGrace
		monitor := newChainBlockMonitor(logger, blockTimeout, drandCtl)
		ws.OnNewBlockHeader(func(handlerCtx context.Context, info chainws.NewBlockHeaderEvent) {
			monitor.OnNewBlock(handlerCtx, info)
		})
		go monitor.Run(ctx)
	} else {
		logger.Info("chain block monitor disabled; consensus.timeout_commit not found in config.toml")
	}
	if !cfg.DebugHTTPEnabled && (len(dkgCfg.JoinerAddrs) > 0 || strings.TrimSpace(dkgCfg.GroupSourceAddr) != "") {
		logger.Warn("debug HTTP server disabled; DKG identity/group endpoints will be unavailable")
	}

	if strings.TrimSpace(cfg.ChainGRPCAddr) != "" {
		go runChainWatcherWithRetry(ctx, logger, metrics, &drandCfg, cfg.ChainGRPCAddr, chainWatchConfig{
			reshareExtraArgs: cfg.DrandReshareArgs,
			reshareEnabled:   cfg.ReshareEnabled,
			reshareTimeout:   cfg.DrandReshareTimeout,
		}, dyn, drandCtl, dkgMgr.Load(), initialDKGEvents, paramsUpdatedEvents, reshareEvents)
	} else {
		logger.Error("chain gRPC address is required; ensure config/client.toml or config/app.toml is present in the chain home")
		return 1
	}

	server := vrfserver.NewServer(dyn, logger, metrics)
	server.RegisterDebugRoutes(func(mux *http.ServeMux) {
		mux.HandleFunc("/vrf/v1/identity", identityHandler(logger, drandCfg, dkgCfg))
		mux.HandleFunc("/vrf/v1/group", groupHandler(drandCfg, dkgCfg))
	})
	if err := server.SetGRPCConfig(cfg.GRPC); err != nil {
		logger.Error("failed to configure gRPC server options", zap.Error(err))
		return 1
	}
	server.RegisterServices(func(reg grpc.ServiceRegistrar) {
		sidecarv1.RegisterDrandControlServer(reg, newDrandControlServer(drandCtl))
	})
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

func ensureStartPrereqs(cfg cliConfig) error {
	if err := ensureUnixListenDir(cfg.ListenAddr); err != nil {
		return err
	}
	if cfg.MetricsEnabled {
		if err := ensureUnixListenDir(cfg.MetricsAddr); err != nil {
			return err
		}
	}
	if cfg.DebugHTTPEnabled {
		if err := ensureUnixListenDir(cfg.DebugHTTPAddr); err != nil {
			return err
		}
	}

	if dir := strings.TrimSpace(cfg.DrandDataDir); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating drand data dir %q: %w", dir, err)
		}
	}

	return nil
}

func ensureUnixListenDir(addr string) error {
	addr = strings.TrimSpace(addr)
	if !strings.HasPrefix(addr, "unix://") {
		return nil
	}
	path := strings.TrimPrefix(addr, "unix://")
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("unix listener path is empty for addr %q", addr)
	}
	return os.MkdirAll(filepath.Dir(path), 0o755)
}

type drandHTTPBeacon struct {
	Round uint64 `json:"round"`
}

func logDrandStatus(
	ctx context.Context,
	logger *zap.Logger,
	dyn *sidecar.DynamicService,
	drandHTTP string,
	interval time.Duration,
) {
	if strings.TrimSpace(drandHTTP) == "" || interval <= 0 {
		return
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	if dyn == nil {
		return
	}

	parsed, err := url.Parse(drandHTTP)
	if err != nil {
		logger.Warn("invalid drand http endpoint; status logger disabled", zap.String("drand_http", drandHTTP), zap.Error(err))
		return
	}

	host := strings.TrimSpace(parsed.Host)
	if host == "" {
		logger.Warn("invalid drand http endpoint; status logger disabled", zap.String("drand_http", drandHTTP))
		return
	}

	if _, _, err := net.SplitHostPort(host); err != nil {
		switch parsed.Scheme {
		case "https":
			host = net.JoinHostPort(host, "443")
		default:
			host = net.JoinHostPort(host, "80")
		}
	}

	client := &http.Client{Timeout: 4 * time.Second}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		conn, dialErr := (&net.Dialer{}).DialContext(dialCtx, "tcp", host)
		cancel()
		if conn != nil {
			_ = conn.Close()
		}

		info, infoErr := dyn.Info(ctx)
		fields := []zap.Field{
			zap.String("drand_http", drandHTTP),
			zap.Bool("reachable", dialErr == nil),
		}

		if infoErr == nil && info != nil && len(info.ChainHash) > 0 {
			chainHashHex := fmt.Sprintf("%x", info.ChainHash)
			fields = append(fields,
				zap.String("chain_hash", chainHashHex),
				zap.Uint64("period_seconds", info.PeriodSeconds),
				zap.Int64("genesis_unix_sec", info.GenesisUnixSec),
			)

			req, err := http.NewRequestWithContext(
				ctx,
				http.MethodGet,
				strings.TrimRight(drandHTTP, "/")+fmt.Sprintf("/%s/public/latest", chainHashHex),
				nil,
			)
			if err == nil {
				resp, err := client.Do(req)
				if err == nil && resp != nil {
					var beacon drandHTTPBeacon
					if resp.StatusCode == http.StatusOK {
						_ = json.NewDecoder(resp.Body).Decode(&beacon)
						fields = append(fields, zap.Uint64("latest_round", beacon.Round))
					} else {
						fields = append(fields, zap.String("latest_round_status", resp.Status))
					}
					_ = resp.Body.Close()
				} else if err != nil {
					fields = append(fields, zap.Error(err))
				}
			}
		}

		logger.Info("drand status", fields...)
	}
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
