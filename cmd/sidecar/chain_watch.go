package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"

	cometrpc "github.com/cometbft/cometbft/rpc/client/http"
	comettypes "github.com/cometbft/cometbft/types"

	vrfv1 "github.com/vexxvakan/vrf/api/vexxvakan/vrf/v1"
	"github.com/vexxvakan/vrf/sidecar"
	sidecarmetrics "github.com/vexxvakan/vrf/sidecar/servers/prometheus/metrics"
	servertypes "github.com/vexxvakan/vrf/sidecar/servers/vrf/types"
)

var (
	errChainRPCAddrRequiredForWS      = errors.New("chain RPC address is required for websocket event subscription")
	errChainReturnedNilVrfParams      = errors.New("chain returned nil VrfParams")
	errNilConfig                      = errors.New("nil config")
	errNilVrfParams                   = errors.New("nil vrf params")
	errOnChainVrfParamsIncomplete     = errors.New("on-chain VrfParams is incomplete: chain_hash, public_key, period_seconds, genesis_unix_sec are required")
	errChainHashMismatch              = errors.New("sidecar config mismatch: drand chain hash does not match on-chain params")
	errPublicKeyMismatch              = errors.New("sidecar config mismatch: drand public key does not match on-chain params")
	errPeriodMismatch                 = errors.New("sidecar config mismatch: drand period does not match on-chain params")
	errGenesisMismatch                = errors.New("sidecar config mismatch: drand genesis does not match on-chain params")
	errDrandDataDirRequiredForReshare = errors.New("drand data dir is required for reshare listener")
	errChainGRPCAddrRequired          = errors.New("chain gRPC address is required")
	errNilSidecarConfig               = errors.New("nil sidecar config")
	errNilDynamicService              = errors.New("nil dynamic service")
)

type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }

func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

type drandController struct {
	cfg     sidecar.DrandProcessConfig
	logger  *zap.Logger
	metrics sidecarmetrics.Metrics

	mu   sync.Mutex
	proc *sidecar.DrandProcess
}

func newDrandController(cfg sidecar.DrandProcessConfig, logger *zap.Logger, metrics sidecarmetrics.Metrics) *drandController {
	if logger == nil {
		logger = zap.NewNop()
	}
	if metrics == nil {
		metrics = sidecarmetrics.NewNop()
	}

	return &drandController{
		cfg:     cfg,
		logger:  logger,
		metrics: metrics,
	}
}

func (c *drandController) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.proc != nil {
		return nil
	}

	proc, err := sidecar.StartDrandProcess(ctx, c.cfg, c.logger, c.metrics)
	if err != nil {
		return err
	}
	c.proc = proc
	return nil
}

func (c *drandController) Stop() {
	c.mu.Lock()
	proc := c.proc
	c.proc = nil
	c.mu.Unlock()

	if proc != nil {
		proc.Stop()
	}
}

type reshareEventInfo struct {
	height    int64
	initiator string
}

type reshareEventCache struct {
	mu      sync.RWMutex
	byEpoch map[uint64]reshareEventInfo
}

func newReshareEventCache() *reshareEventCache {
	return &reshareEventCache{byEpoch: make(map[uint64]reshareEventInfo)}
}

func (c *reshareEventCache) set(epoch uint64, info reshareEventInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byEpoch[epoch] = info
}

func (c *reshareEventCache) get(epoch uint64) (reshareEventInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	info, ok := c.byEpoch[epoch]
	return info, ok
}

func startReshareEventSubscriber(
	ctx context.Context,
	logger *zap.Logger,
	cometRPCAddr string,
) (*reshareEventCache, func(), error) {
	if strings.TrimSpace(cometRPCAddr) == "" {
		return nil, nil, errChainRPCAddrRequiredForWS
	}

	client, err := cometrpc.New(cometRPCAddr, "/websocket")
	if err != nil {
		return nil, nil, err
	}

	if err := client.Start(); err != nil {
		return nil, nil, err
	}

	cache := newReshareEventCache()

	sub, err := client.Subscribe(ctx, "sidecar", "tm.event='Tx'", 256)
	if err != nil {
		_ = client.Stop()
		return nil, nil, err
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-sub:
				if !ok {
					return
				}

				epochs := ev.Events["vrf_schedule_reshare.new_reshare_epoch"]
				if len(epochs) == 0 {
					continue
				}

				epoch, err := strconv.ParseUint(epochs[0], 10, 64)
				if err != nil {
					continue
				}

				info := reshareEventInfo{}
				if initiators := ev.Events["vrf_schedule_reshare.initiator"]; len(initiators) > 0 {
					info.initiator = initiators[0]
				}

				switch data := ev.Data.(type) {
				case comettypes.EventDataTx:
					info.height = data.Height
				case *comettypes.EventDataTx:
					info.height = data.Height
				}

				cache.set(epoch, info)
				logger.Info(
					"observed vrf reshare event",
					zap.Uint64("reshare_epoch", epoch),
					zap.Int64("height", info.height),
					zap.String("initiator", info.initiator),
				)
			}
		}
	}()

	closeFn := func() {
		_ = client.UnsubscribeAll(context.Background(), "sidecar")
		_ = client.Stop()
	}

	return cache, closeFn, nil
}

func queryVrfParams(ctx context.Context, qc vrfv1.QueryClient) (*vrfv1.VrfParams, error) {
	resp, err := qc.Params(ctx, &vrfv1.QueryParamsRequest{})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Params == nil {
		return nil, errChainReturnedNilVrfParams
	}
	return resp.Params, nil
}

func mergeChainParamsIntoConfig(cfg *sidecar.Config, params *vrfv1.VrfParams) error {
	if cfg == nil {
		return errNilConfig
	}

	if params == nil {
		return errNilVrfParams
	}

	if len(params.ChainHash) == 0 || len(params.PublicKey) == 0 || params.PeriodSeconds == 0 || params.GenesisUnixSec == 0 {
		return errOnChainVrfParamsIncomplete
	}

	if len(cfg.ChainHash) > 0 && !bytes.Equal(cfg.ChainHash, params.ChainHash) {
		return errChainHashMismatch
	}
	if len(cfg.PublicKey) > 0 && !bytes.Equal(cfg.PublicKey, params.PublicKey) {
		return errPublicKeyMismatch
	}
	if cfg.PeriodSeconds != 0 && cfg.PeriodSeconds != params.PeriodSeconds {
		return errPeriodMismatch
	}
	if cfg.GenesisUnixSec != 0 && cfg.GenesisUnixSec != params.GenesisUnixSec {
		return errGenesisMismatch
	}

	cfg.ChainHash = append([]byte(nil), params.ChainHash...)
	cfg.PublicKey = append([]byte(nil), params.PublicKey...)
	cfg.PeriodSeconds = params.PeriodSeconds
	cfg.GenesisUnixSec = params.GenesisUnixSec

	return nil
}

func infoFromConfig(cfg sidecar.Config) *servertypes.QueryInfoResponse {
	return &servertypes.QueryInfoResponse{
		ChainHash:      append([]byte(nil), cfg.ChainHash...),
		PublicKey:      append([]byte(nil), cfg.PublicKey...),
		PeriodSeconds:  cfg.PeriodSeconds,
		GenesisUnixSec: cfg.GenesisUnixSec,
	}
}

func dialChainGRPC(ctx context.Context, addr string) (*grpc.ClientConn, vrfv1.QueryClient, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}

	conn.Connect()
	for {
		state := conn.GetState()
		if state == connectivity.Ready {
			break
		}

		if !conn.WaitForStateChange(ctx, state) {
			_ = conn.Close()
			return nil, nil, ctx.Err()
		}
	}

	return conn, vrfv1.NewQueryClient(conn), nil
}

type reshareRunner struct {
	enabled bool

	drandBinary string
	dataDir     string
	extraArgs   []string
	timeout     time.Duration

	stateFile string
}

func newReshareRunner(enabled bool, cfg sidecar.Config, extraArgs []string, timeout time.Duration) (*reshareRunner, error) {
	if !enabled {
		return &reshareRunner{enabled: false}, nil
	}

	if strings.TrimSpace(cfg.DrandDataDir) == "" {
		return nil, errDrandDataDirRequiredForReshare
	}

	bin := strings.TrimSpace(cfg.BinaryPath)
	if bin == "" {
		bin = "drand"
	}

	if timeout <= 0 {
		timeout = 30 * time.Minute
	}

	return &reshareRunner{
		enabled:     true,
		drandBinary: bin,
		dataDir:     cfg.DrandDataDir,
		extraArgs:   append([]string(nil), extraArgs...),
		timeout:     timeout,
		stateFile:   filepath.Join(cfg.DrandDataDir, "sidecar_last_reshare_epoch"),
	}, nil
}

func (r *reshareRunner) isEnabled() bool {
	return r != nil && r.enabled
}

func (r *reshareRunner) lastEpoch() (uint64, error) {
	if !r.enabled {
		return 0, nil
	}

	b, err := os.ReadFile(r.stateFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}

	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0, nil
	}

	epoch, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, err
	}

	return epoch, nil
}

func (r *reshareRunner) recordEpoch(epoch uint64) error {
	if !r.enabled {
		return nil
	}

	tmp := r.stateFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatUint(epoch, 10)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.stateFile)
}

func (r *reshareRunner) run(ctx context.Context, logger *zap.Logger) error {
	if !r.enabled {
		return nil
	}

	cmdCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	args := []string{"share", "--folder", r.dataDir, "--reshare"}
	args = append(args, r.extraArgs...)

	cmd := exec.CommandContext(cmdCtx, r.drandBinary, args...) //nolint:gosec

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	go pipeToLogger(stdout, logger, "stdout")
	go pipeToLogger(stderr, logger, "stderr")

	return cmd.Wait()
}

func pipeToLogger(r io.ReadCloser, logger *zap.Logger, stream string) {
	defer func() { _ = r.Close() }()

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		logger.Info("drand reshare", zap.String("stream", stream), zap.String("line", line))
	}
}

type chainWatchConfig struct {
	pollInterval time.Duration
	wsEnabled    bool
	cometRPCAddr string

	reshareExtraArgs []string
	reshareEnabled   bool
	reshareTimeout   time.Duration
}

func startChainWatcher(
	ctx context.Context,
	cancel context.CancelFunc,
	logger *zap.Logger,
	metrics sidecarmetrics.Metrics,
	cfg *sidecar.Config,
	chainGRPCAddr string,
	opts chainWatchConfig,
	dyn *sidecar.DynamicService,
	drandCtl *drandController,
) (initialEnabled bool, initialReshareEpoch uint64, cleanup func(), err error) {
	if strings.TrimSpace(chainGRPCAddr) == "" {
		return false, 0, func() {}, errChainGRPCAddrRequired
	}

	if cfg == nil {
		return false, 0, func() {}, errNilSidecarConfig
	}

	if logger == nil {
		logger = zap.NewNop()
	}
	if metrics == nil {
		metrics = sidecarmetrics.NewNop()
	}
	if dyn == nil {
		return false, 0, func() {}, errNilDynamicService
	}

	conn, qc, err := dialChainGRPC(ctx, chainGRPCAddr)
	if err != nil {
		return false, 0, func() {}, err
	}

	params, err := queryVrfParams(ctx, qc)
	if err != nil {
		_ = conn.Close()
		return false, 0, func() {}, err
	}

	if err := mergeChainParamsIntoConfig(cfg, params); err != nil {
		_ = conn.Close()
		return false, 0, func() {}, err
	}

	cfgSnapshot := *cfg
	dyn.SetInfo(infoFromConfig(cfgSnapshot))

	var (
		eventCache *reshareEventCache
		closeWS    func()
	)
	if opts.wsEnabled {
		cache, closeFn, wsErr := startReshareEventSubscriber(ctx, logger, opts.cometRPCAddr)
		if wsErr != nil {
			_ = conn.Close()
			return false, 0, func() {}, wsErr
		}
		eventCache = cache
		closeWS = closeFn
	}

	runner, err := newReshareRunner(opts.reshareEnabled, cfgSnapshot, opts.reshareExtraArgs, opts.reshareTimeout)
	if err != nil {
		if closeWS != nil {
			closeWS()
		}
		_ = conn.Close()
		return false, 0, func() {}, err
	}

	enabled := params.Enabled
	reshareEpoch := params.ReshareEpoch

	if opts.pollInterval <= 0 {
		opts.pollInterval = 5 * time.Second
	}

	cleanup = func() {
		if closeWS != nil {
			closeWS()
		}
		_ = conn.Close()
	}

	runReshare := func(oldEpoch, newEpoch uint64, evt reshareEventInfo) bool {
		if !runner.isEnabled() {
			logger.Info(
				"reshare detected but listener disabled; skipping",
				zap.Uint64("old_reshare_epoch", oldEpoch),
				zap.Uint64("new_reshare_epoch", newEpoch),
				zap.Int64("height", evt.height),
				zap.String("initiator", evt.initiator),
			)
			return false
		}

		lastDone, err := runner.lastEpoch()
		if err != nil {
			logger.Error("failed to load last reshare epoch", zap.Error(err))
			return false
		}
		if lastDone >= newEpoch {
			logger.Info("reshare already executed; skipping", zap.Uint64("reshare_epoch", newEpoch))
			return false
		}

		logger.Info(
			"starting drand reshare",
			zap.Uint64("old_reshare_epoch", oldEpoch),
			zap.Uint64("new_reshare_epoch", newEpoch),
			zap.Int64("height", evt.height),
			zap.String("initiator", evt.initiator),
		)

		dyn.SetService(nil)
		if drandCtl != nil {
			drandCtl.Stop()
		}

		if err := runner.run(ctx, logger); err != nil {
			logger.Error("drand reshare command failed", zap.Error(err))
			cancel()
			return false
		}

		if err := runner.recordEpoch(newEpoch); err != nil {
			logger.Error("failed to record completed reshare epoch", zap.Error(err))
		}

		if enabled {
			if drandCtl != nil {
				if err := drandCtl.Start(ctx); err != nil {
					logger.Error("failed to restart drand subprocess after reshare", zap.Error(err))
					cancel()
					return false
				}
			}

			svc, err := newDrandServiceWithRetry(ctx, cfgSnapshot, logger, metrics)
			if err != nil {
				logger.Error("failed to recreate drand service after reshare", zap.Error(err))
				cancel()
				return false
			}
			dyn.SetService(svc)
		}

		logger.Info(
			"drand reshare completed",
			zap.Uint64("reshare_epoch", newEpoch),
			zap.Bool("enabled", enabled),
		)
		return true
	}

	// Catch-up: if the node restarts after a reshare was scheduled, run it once.
	reshareExecutedAtStartup := false
	if runner.isEnabled() {
		lastDone, err := runner.lastEpoch()
		if err != nil {
			cleanup()
			return false, 0, func() {}, err
		}
		if lastDone < reshareEpoch && reshareEpoch > 0 {
			evt := reshareEventInfo{}
			if eventCache != nil {
				if info, ok := eventCache.get(reshareEpoch); ok {
					evt = info
				}
			}
			reshareExecutedAtStartup = runReshare(lastDone, reshareEpoch, evt)
		}
	}

	// Apply initial enabled state.
	if !enabled {
		if drandCtl != nil {
			drandCtl.Stop()
		}
		dyn.SetService(nil)
	} else {
		if drandCtl != nil {
			if err := drandCtl.Start(ctx); err != nil {
				cleanup()
				return false, 0, func() {}, err
			}
		}

		if !reshareExecutedAtStartup {
			svc, err := newDrandServiceWithRetry(ctx, cfgSnapshot, logger, metrics)
			if err != nil {
				cleanup()
				return false, 0, func() {}, err
			}
			dyn.SetService(svc)
		}
	}

	go func() {
		ticker := time.NewTicker(opts.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			p, err := queryVrfParams(ctx, qc)
			if err != nil {
				logger.Warn("failed to query on-chain VRF params", zap.Error(err))
				continue
			}

			// Fail fast if the on-chain crypto/timing context changes under our feet.
			if !bytes.Equal(p.ChainHash, cfgSnapshot.ChainHash) ||
				!bytes.Equal(p.PublicKey, cfgSnapshot.PublicKey) ||
				p.PeriodSeconds != cfgSnapshot.PeriodSeconds ||
				p.GenesisUnixSec != cfgSnapshot.GenesisUnixSec {
				logger.Error("on-chain VRF params changed; refusing to continue")
				cancel()
				return
			}

			if p.Enabled != enabled {
				enabled = p.Enabled
				logger.Info("on-chain VRF enabled changed", zap.Bool("enabled", enabled))

				if !enabled {
					if drandCtl != nil {
						drandCtl.Stop()
					}
					dyn.SetService(nil)
				} else {
					if drandCtl != nil {
						if err := drandCtl.Start(ctx); err != nil {
							logger.Error("failed to start drand subprocess after enable", zap.Error(err))
						}
					}

					svc, err := newDrandServiceWithRetry(ctx, cfgSnapshot, logger, metrics)
					if err != nil {
						logger.Error("failed to create drand service after enable", zap.Error(err))
					} else {
						dyn.SetService(svc)
					}
				}
			}

			if p.ReshareEpoch > reshareEpoch {
				newEpoch := p.ReshareEpoch
				oldEpoch := reshareEpoch
				reshareEpoch = newEpoch

				evt := reshareEventInfo{}
				if eventCache != nil {
					if info, ok := eventCache.get(newEpoch); ok {
						evt = info
					}
				}
				_ = runReshare(oldEpoch, newEpoch, evt)
			}
		}
	}()

	return enabled, reshareEpoch, cleanup, nil
}
