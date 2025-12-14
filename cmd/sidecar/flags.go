package main

import (
	"errors"
	"flag"
	"fmt"
	"time"
)

var (
	errSidecarNonLoopbackBind   = errors.New("refusing to bind sidecar to non-loopback address without --vrf-allow-public-bind")
	errMetricsNonLoopbackBind   = errors.New("refusing to bind metrics to non-loopback address without --vrf-allow-public-bind")
	errDebugHTTPNonLoopbackBind = errors.New("refusing to bind debug http to non-loopback address without --vrf-allow-public-bind")
)

type cliConfig struct {
	ListenAddr      string
	AllowPublicBind bool

	MetricsEnabled bool
	MetricsAddr    string
	ChainID        string

	DebugHTTPEnabled bool
	DebugHTTPAddr    string

	DrandSupervise     bool
	DrandHTTP          string
	DrandPublicAddr    string
	DrandPrivateAddr   string
	DrandControlAddr   string
	DrandDataDir       string
	DrandBinary        string
	DrandVersionCheck  string
	DrandChainHashHex  string
	DrandPublicKeyB64  string
	DrandPeriodSeconds uint
	DrandGenesisUnix   int64

	ChainGRPCAddr       string
	ChainRPCAddr        string
	ChainPollInterval   time.Duration
	ChainWSEnabled      bool
	ReshareEnabled      bool
	DrandReshareTimeout time.Duration
	DrandReshareArgs    []string
}

func parseFlags(args []string) (cliConfig, bool, error) {
	fs := flag.NewFlagSet("sidecar", flag.ContinueOnError)

	showVersion := fs.Bool("version", false, "print version and exit")

	listenAddr := fs.String("listen-addr", "127.0.0.1:8090", "sidecar gRPC listen address (loopback or UDS via unix://)")
	allowPublic := fs.Bool("vrf-allow-public-bind", false, "allow sidecar to bind to non-loopback addresses (unsafe; operators must secure access)")

	metricsEnabled := fs.Bool("metrics-enabled", false, "enable Prometheus metrics")
	metricsAddr := fs.String("metrics-addr", "127.0.0.1:8091", "Prometheus metrics listen address (loopback or UDS via unix://; socket perms via umask/ownership)")
	chainID := fs.String("chain-id", "", "chain ID label for metrics (optional)")

	debugHTTPEnabled := fs.Bool("debug-http-enabled", false, "enable debug HTTP/JSON server")
	debugHTTPAddr := fs.String("debug-http-addr", "127.0.0.1:8092", "debug HTTP/JSON listen address (loopback or UDS via unix://)")

	drandSupervise := fs.Bool("drand-supervise", true, "start and supervise a local drand subprocess")
	drandHTTP := fs.String("drand-http", "", "drand HTTP base URL (defaults to http://<drand-public-addr>)")
	drandPublic := fs.String("drand-public-addr", "127.0.0.1:8081", "drand public listen address (also used for HTTP)")
	drandPrivate := fs.String("drand-private-addr", "0.0.0.0:4444", "drand private listen address")
	drandControl := fs.String("drand-control-addr", "127.0.0.1:8881", "drand control listen address")
	drandDataDir := fs.String("drand-data-dir", "", "drand data directory (required when --drand-supervise)")

	drandBinary := fs.String("drand-binary", "drand", "path to drand binary")
	drandVersionCheck := fs.String("drand-version-check", "strict", "drand version check mode (strict|off)")
	chainHashHex := fs.String("drand-chain-hash", "", "expected drand chain hash (hex)")
	publicKeyB64 := fs.String("drand-public-key", "", "expected drand group public key (base64)")
	periodSeconds := fs.Uint("drand-period-seconds", 0, "drand beacon period in seconds")
	genesisUnix := fs.Int64("drand-genesis-unix", 0, "drand genesis time (unix seconds)")

	chainGRPCAddr := fs.String("chain-grpc-addr", "", "optional chain gRPC address (x/vrf params watcher)")
	chainRPCAddr := fs.String("chain-rpc-addr", "", "optional CometBFT RPC address (required for --chain-ws-enabled)")
	chainPollInterval := fs.Duration("chain-poll-interval", 5*time.Second, "poll interval for on-chain VRF params watcher")
	chainWSEnabled := fs.Bool("chain-ws-enabled", false, "enable websocket subscription for vrf_schedule_reshare events (requires --chain-rpc-addr)")

	reshareEnabled := fs.Bool("reshare-enabled", true, "enable drand reshare listener (recommended/required for validator ops)")
	reshareTimeout := fs.Duration("drand-reshare-timeout", 30*time.Minute, "timeout for drand reshare command execution")
	var reshareArgs stringSliceFlag
	fs.Var(&reshareArgs, "drand-reshare-arg", "additional arg to pass to `drand share --reshare` (repeatable)")

	if err := fs.Parse(args); err != nil {
		return cliConfig{}, false, err
	}

	return cliConfig{
		ListenAddr:          *listenAddr,
		AllowPublicBind:     *allowPublic,
		MetricsEnabled:      *metricsEnabled,
		MetricsAddr:         *metricsAddr,
		ChainID:             *chainID,
		DebugHTTPEnabled:    *debugHTTPEnabled,
		DebugHTTPAddr:       *debugHTTPAddr,
		DrandSupervise:      *drandSupervise,
		DrandHTTP:           *drandHTTP,
		DrandPublicAddr:     *drandPublic,
		DrandPrivateAddr:    *drandPrivate,
		DrandControlAddr:    *drandControl,
		DrandDataDir:        *drandDataDir,
		DrandBinary:         *drandBinary,
		DrandVersionCheck:   *drandVersionCheck,
		DrandChainHashHex:   *chainHashHex,
		DrandPublicKeyB64:   *publicKeyB64,
		DrandPeriodSeconds:  *periodSeconds,
		DrandGenesisUnix:    *genesisUnix,
		ChainGRPCAddr:       *chainGRPCAddr,
		ChainRPCAddr:        *chainRPCAddr,
		ChainPollInterval:   *chainPollInterval,
		ChainWSEnabled:      *chainWSEnabled,
		ReshareEnabled:      *reshareEnabled,
		DrandReshareTimeout: *reshareTimeout,
		DrandReshareArgs:    []string(reshareArgs),
	}, *showVersion, nil
}

func validateBindConfig(cfg cliConfig) error {
	if !cfg.AllowPublicBind && !isLoopbackAddr(cfg.ListenAddr) {
		return fmt.Errorf("%w (addr=%s)", errSidecarNonLoopbackBind, cfg.ListenAddr)
	}

	if cfg.MetricsEnabled && !cfg.AllowPublicBind && !isLoopbackAddr(cfg.MetricsAddr) {
		return fmt.Errorf("%w (addr=%s)", errMetricsNonLoopbackBind, cfg.MetricsAddr)
	}

	if cfg.DebugHTTPEnabled && !cfg.AllowPublicBind && !isLoopbackAddr(cfg.DebugHTTPAddr) {
		return fmt.Errorf("%w (addr=%s)", errDebugHTTPNonLoopbackBind, cfg.DebugHTTPAddr)
	}

	return nil
}
