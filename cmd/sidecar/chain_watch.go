package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"

	sidecarv1 "github.com/vexxvakan/vrf/api/vexxvakan/sidecar/v1"
	vrfv1 "github.com/vexxvakan/vrf/api/vexxvakan/vrf/v1"
	"github.com/vexxvakan/vrf/sidecar"
	"github.com/vexxvakan/vrf/sidecar/chainws"
	"github.com/vexxvakan/vrf/sidecar/drand"
	sidecarmetrics "github.com/vexxvakan/vrf/sidecar/servers/metrics"
)

var (
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
	errNilDKGManager                  = errors.New("nil dkg manager")
)

type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }

func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

type drandController struct {
	cfg     drand.DrandProcessConfig
	logger  *zap.Logger
	metrics sidecarmetrics.Metrics

	mu   sync.Mutex
	proc *drand.DrandProcess
}

func newDrandController(cfg drand.DrandProcessConfig, logger *zap.Logger, metrics sidecarmetrics.Metrics) *drandController {
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

	proc, err := drand.StartDrandProcess(ctx, c.cfg, c.logger, c.metrics)
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

func (c *drandController) Status() drand.ProcessStatus {
	if c == nil {
		return drand.ProcessStatus{}
	}

	c.mu.Lock()
	proc := c.proc
	c.mu.Unlock()

	if proc == nil {
		return drand.ProcessStatus{}
	}

	return proc.Status()
}

func (c *drandController) TailLogs(n int) []drand.LogEntry {
	if c == nil {
		return nil
	}

	c.mu.Lock()
	proc := c.proc
	c.mu.Unlock()

	if proc == nil {
		return nil
	}

	return proc.TailLogs(n)
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

func mergeChainParamsIntoConfig(cfg *drand.Config, params *vrfv1.VrfParams) error {
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

func mergeInitialDKGIntoConfig(cfg *drand.Config, info chainws.InitialDKGEvent) error {
	if cfg == nil {
		return errNilConfig
	}

	if len(info.ChainHash) == 0 || len(info.PublicKey) == 0 || info.PeriodSeconds == 0 || info.GenesisUnixSec == 0 {
		return errOnChainVrfParamsIncomplete
	}

	if len(cfg.ChainHash) > 0 && !bytes.Equal(cfg.ChainHash, info.ChainHash) {
		return errChainHashMismatch
	}
	if len(cfg.PublicKey) > 0 && !bytes.Equal(cfg.PublicKey, info.PublicKey) {
		return errPublicKeyMismatch
	}
	if cfg.PeriodSeconds != 0 && cfg.PeriodSeconds != info.PeriodSeconds {
		return errPeriodMismatch
	}
	if cfg.GenesisUnixSec != 0 && cfg.GenesisUnixSec != info.GenesisUnixSec {
		return errGenesisMismatch
	}

	cfg.ChainHash = append([]byte(nil), info.ChainHash...)
	cfg.PublicKey = append([]byte(nil), info.PublicKey...)
	cfg.PeriodSeconds = info.PeriodSeconds
	cfg.GenesisUnixSec = info.GenesisUnixSec

	return nil
}

func clearChainParams(cfg *drand.Config) {
	if cfg == nil {
		return
	}

	cfg.ChainHash = nil
	cfg.PublicKey = nil
	cfg.PeriodSeconds = 0
	cfg.GenesisUnixSec = 0
}

func infoFromConfig(cfg drand.Config) *sidecarv1.QueryInfoResponse {
	return &sidecarv1.QueryInfoResponse{
		ChainHash:      append([]byte(nil), cfg.ChainHash...),
		PublicKey:      append([]byte(nil), cfg.PublicKey...),
		PeriodSeconds:  cfg.PeriodSeconds,
		GenesisUnixSec: cfg.GenesisUnixSec,
	}
}

func chainConfigComplete(cfg drand.Config) bool {
	return len(cfg.ChainHash) > 0 &&
		len(cfg.PublicKey) > 0 &&
		cfg.PeriodSeconds > 0 &&
		cfg.GenesisUnixSec > 0
}

func dialChainGRPC(ctx context.Context, addr string) (*grpc.ClientConn, vrfv1.QueryClient, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

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

		if !conn.WaitForStateChange(dialCtx, state) {
			_ = conn.Close()
			return nil, nil, dialCtx.Err()
		}
	}

	return conn, vrfv1.NewQueryClient(conn), nil
}

type reshareRunner struct {
	enabled bool
	manager *dkgManager

	stateFile string
}

func newReshareRunner(enabled bool, cfg drand.Config, manager *dkgManager) (*reshareRunner, error) {
	if !enabled {
		return &reshareRunner{enabled: false}, nil
	}

	if strings.TrimSpace(cfg.DrandDataDir) == "" {
		return nil, errDrandDataDirRequiredForReshare
	}
	if manager == nil {
		return nil, errNilDKGManager
	}

	return &reshareRunner{
		enabled:   true,
		manager:   manager,
		stateFile: filepath.Join(cfg.DrandDataDir, "sidecar_last_reshare_epoch"),
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

func (r *reshareRunner) run(ctx context.Context, evt chainws.ReshareScheduledEvent) error {
	if !r.enabled {
		return nil
	}
	if r.manager == nil {
		return errNilDKGManager
	}
	return r.manager.RunReshare(ctx, evt)
}

type chainWatchConfig struct {
	reshareExtraArgs []string
	reshareEnabled   bool
	reshareTimeout   time.Duration
}

func startChainWatcher(
	ctx context.Context,
	logger *zap.Logger,
	metrics sidecarmetrics.Metrics,
	cfg *drand.Config,
	chainGRPCAddr string,
	opts chainWatchConfig,
	dyn *sidecar.DynamicService,
	drandCtl *drandController,
	dkgMgr *dkgManager,
	initialDKGEvents <-chan chainws.InitialDKGEvent,
	paramsUpdatedEvents <-chan chainws.ParamsUpdatedEvent,
	reshareEvents <-chan chainws.ReshareScheduledEvent,
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

	cfgSnapshot := *cfg
	if err := mergeChainParamsIntoConfig(&cfgSnapshot, params); err != nil {
		if errors.Is(err, errOnChainVrfParamsIncomplete) {
			logger.Info("on-chain VRF params incomplete; waiting for initialization")
		} else {
			logger.Warn("failed to merge on-chain VRF params into sidecar config; starting in idle mode", zap.Error(err))
			clearChainParams(&cfgSnapshot)
		}
	}
	dyn.SetInfo(infoFromConfig(cfgSnapshot))

	runner, err := newReshareRunner(opts.reshareEnabled, cfgSnapshot, dkgMgr)
	if err != nil {
		_ = conn.Close()
		return false, 0, func() {}, err
	}

	enabled := params.Enabled
	reshareEpoch := params.ReshareEpoch
	serviceActive := false

	cleanup = func() {
		_ = conn.Close()
	}

	runReshare := func(oldEpoch, newEpoch uint64, evt chainws.ReshareScheduledEvent) bool {
		if !runner.isEnabled() {
			logger.Info(
				"reshare detected but listener disabled; skipping",
				zap.Uint64("old_reshare_epoch", oldEpoch),
				zap.Uint64("new_reshare_epoch", newEpoch),
				zap.Int64("height", evt.Height),
				zap.String("scheduler", evt.Scheduler),
				zap.String("reason", evt.Reason),
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
			zap.Int64("height", evt.Height),
			zap.String("scheduler", evt.Scheduler),
			zap.String("reason", evt.Reason),
		)

		dyn.SetService(nil)
		serviceActive = false

		if err := runner.run(ctx, evt); err != nil {
			logger.Error("drand reshare command failed", zap.Error(err))
			return false
		}

		if err := runner.recordEpoch(newEpoch); err != nil {
			logger.Error("failed to record completed reshare epoch", zap.Error(err))
		}

		logger.Info(
			"drand reshare completed",
			zap.Uint64("reshare_epoch", newEpoch),
			zap.Bool("enabled", enabled),
			zap.Int64("height", evt.Height),
			zap.String("scheduler", evt.Scheduler),
			zap.String("reason", evt.Reason),
		)
		return true
	}

	// Catch-up: if the node restarts after a reshare was scheduled, run it once.
	if runner.isEnabled() {
		lastDone, err := runner.lastEpoch()
		if err != nil {
			cleanup()
			return false, 0, func() {}, err
		}
		desiredEpoch := reshareEpoch
		if lastDone < desiredEpoch && desiredEpoch > 0 {
			evt := chainws.ReshareScheduledEvent{OldEpoch: lastDone, NewEpoch: desiredEpoch}
			if runReshare(lastDone, desiredEpoch, evt) {
				reshareEpoch = desiredEpoch
			} else {
				reshareEpoch = lastDone
			}
		} else if lastDone > desiredEpoch {
			reshareEpoch = lastDone
		}
	}

	// Keep the drand daemon running while we wait for initial DKG / param setup.
	if drandCtl != nil {
		if err := drandCtl.Start(ctx); err != nil {
			logger.Error("failed to start drand subprocess; continuing in idle mode", zap.Error(err))
		}
	}

	// Apply initial enabled state.
	if enabled {
		if chainConfigComplete(cfgSnapshot) {
			logger.Info("on-chain VRF enabled; waiting for drand service to become ready")
		} else {
			logger.Info("on-chain VRF enabled but params are incomplete; service will start once initialized")
		}
	}

	tryStartService := func() {
		if !chainConfigComplete(cfgSnapshot) || serviceActive {
			return
		}

		svc, err := drand.NewDrandService(ctx, cfgSnapshot, logger, metrics)
		if err != nil {
			logger.Warn("drand service not ready yet; waiting", zap.Error(err))
			return
		}

		dyn.SetService(svc)
		serviceActive = true
		logger.Info("drand service is ready")
	}

	tryStartService()

	go func() {
		serviceTicker := time.NewTicker(2 * time.Second)
		defer serviceTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case evt := <-initialDKGEvents:
				if err := mergeInitialDKGIntoConfig(&cfgSnapshot, evt); err != nil {
					logger.Warn("failed to merge initial DKG info into config; continuing in idle mode", zap.Error(err))
				} else {
					logger.Info(
						"on-chain VRF params updated after initial DKG",
						zap.Int64("height", evt.Height),
						zap.String("initiator", evt.Initiator),
					)
				}

				if evt.ReshareEpoch > 0 {
					reshareEpoch = evt.ReshareEpoch
				}

				dyn.SetInfo(infoFromConfig(cfgSnapshot))
				tryStartService()
			case evt := <-paramsUpdatedEvents:
				if evt.Params == nil {
					continue
				}

				enabled = evt.Params.Enabled
				reshareEpoch = evt.Params.ReshareEpoch

				if err := mergeChainParamsIntoConfig(&cfgSnapshot, evt.Params); err != nil {
					if errors.Is(err, errOnChainVrfParamsIncomplete) {
						logger.Info("on-chain VRF params incomplete; continuing in idle mode")
					} else {
						logger.Warn("failed to merge on-chain VRF params update; continuing in idle mode", zap.Error(err))
					}
				} else {
					logger.Info(
						"observed vrf params update",
						zap.Int64("height", evt.Height),
						zap.String("authority", evt.Authority),
					)
				}

				dyn.SetInfo(infoFromConfig(cfgSnapshot))
				tryStartService()
			case evt := <-reshareEvents:
				if evt.NewEpoch == 0 {
					continue
				}
				if evt.OldEpoch == 0 && reshareEpoch > 0 {
					evt.OldEpoch = reshareEpoch
				}
				if evt.NewEpoch <= reshareEpoch {
					logger.Debug(
						"ignoring vrf reshare event (not newer than current)",
						zap.Uint64("current_reshare_epoch", reshareEpoch),
						zap.Uint64("new_reshare_epoch", evt.NewEpoch),
						zap.Int64("height", evt.Height),
						zap.String("scheduler", evt.Scheduler),
					)
					continue
				}

				logger.Info(
					"observed vrf reshare event",
					zap.Uint64("old_reshare_epoch", evt.OldEpoch),
					zap.Uint64("new_reshare_epoch", evt.NewEpoch),
					zap.Int64("height", evt.Height),
					zap.String("scheduler", evt.Scheduler),
					zap.String("reason", evt.Reason),
				)

				if runReshare(reshareEpoch, evt.NewEpoch, evt) {
					reshareEpoch = evt.NewEpoch
				}
				continue
			case <-serviceTicker.C:
				tryStartService()
			}
		}
	}()

	return enabled, reshareEpoch, cleanup, nil
}

func runChainWatcherWithRetry(
	ctx context.Context,
	logger *zap.Logger,
	metrics sidecarmetrics.Metrics,
	cfg *drand.Config,
	chainGRPCAddr string,
	opts chainWatchConfig,
	dyn *sidecar.DynamicService,
	drandCtl *drandController,
	dkgMgr *dkgManager,
	initialDKGEvents <-chan chainws.InitialDKGEvent,
	paramsUpdatedEvents <-chan chainws.ParamsUpdatedEvent,
	reshareEvents <-chan chainws.ReshareScheduledEvent,
) {
	if logger == nil {
		logger = zap.NewNop()
	}

	backoff := 500 * time.Millisecond
	maxBackoff := 15 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		_, _, cleanup, err := startChainWatcher(ctx, logger, metrics, cfg, chainGRPCAddr, opts, dyn, drandCtl, dkgMgr, initialDKGEvents, paramsUpdatedEvents, reshareEvents)
		if err == nil {
			<-ctx.Done()
			cleanup()
			return
		}

		logger.Warn("failed to start chain watcher; retrying", zap.Error(err))
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
