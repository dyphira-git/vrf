package main

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"time"

	vrfserver "github.com/vexxvakan/vrf/sidecar/servers/vrf"
)

var (
	errSidecarNonLoopbackBind   = errors.New("refusing to bind sidecar to non-loopback address without --vrf-allow-public-bind")
	errMetricsNonLoopbackBind   = errors.New("refusing to bind metrics to non-loopback address without --vrf-allow-public-bind")
	errDebugHTTPNonLoopbackBind = errors.New("refusing to bind debug http to non-loopback address without --vrf-allow-public-bind")
	errGRPCMaxConcurrentStreams = errors.New("grpc max concurrent streams exceeds uint32 range")
)

type cliConfig struct {
	ListenAddr      string
	AllowPublicBind bool
	GRPC            vrfserver.GRPCServerConfig

	MetricsEnabled bool
	MetricsAddr    string
	ChainID        string

	DebugHTTPEnabled bool
	DebugHTTPAddr    string

	DrandSupervise     bool
	DrandHTTP          string
	DrandAllowNonLoop  bool
	DrandPublicAddr    string
	DrandPrivateAddr   string
	DrandControlAddr   string
	DrandDataDir       string
	DrandBinary        string
	DrandNoRestart     bool
	DrandRestartMin    time.Duration
	DrandRestartMax    time.Duration
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

	grpcKeepaliveTime := fs.Duration("grpc-keepalive-time", 0, "gRPC keepalive ping interval (0 = gRPC default)")
	grpcKeepaliveTimeout := fs.Duration("grpc-keepalive-timeout", 0, "gRPC keepalive ping timeout (0 = gRPC default)")
	grpcKeepaliveMinTime := fs.Duration("grpc-keepalive-min-time", 0, "gRPC minimum time between client keepalive pings (0 = gRPC default)")
	grpcKeepalivePermitWithoutStream := fs.Bool("grpc-keepalive-permit-without-stream", false, "permit keepalive pings without active streams (default false)")
	grpcMaxConnectionIdle := fs.Duration("grpc-max-connection-idle", 0, "gRPC max idle connection duration before GOAWAY (0 = infinity)")
	grpcMaxConnectionAge := fs.Duration("grpc-max-connection-age", 0, "gRPC max connection age before GOAWAY (0 = infinity)")
	grpcMaxConnectionAgeGrace := fs.Duration("grpc-max-connection-age-grace", 0, "gRPC additional grace after max age before force close (0 = infinity)")
	grpcMaxConcurrentStreams := fs.Uint("grpc-max-concurrent-streams", 0, "gRPC max concurrent streams per transport (0 = gRPC default)")
	grpcMaxRecvMsgSize := fs.Int("grpc-max-recv-msg-size", 0, "gRPC max receive message size in bytes (0 = gRPC default)")
	grpcMaxSendMsgSize := fs.Int("grpc-max-send-msg-size", 0, "gRPC max send message size in bytes (0 = gRPC default)")

	drandSupervise := fs.Bool("drand-supervise", true, "start and supervise a local drand subprocess")
	drandHTTP := fs.String("drand-http", "", "drand HTTP base URL (defaults to http://<drand-public-addr>)")
	drandAllowNonLoop := fs.Bool("drand-allow-non-loopback-http", false, "allow drand-http to point at a non-loopback host (unsafe; intended for containerized dev)")
	drandPublic := fs.String("drand-public-addr", "127.0.0.1:8081", "drand public listen address (also used for HTTP)")
	drandPrivate := fs.String("drand-private-addr", "0.0.0.0:4444", "drand private listen address")
	drandControl := fs.String("drand-control-addr", "127.0.0.1:8881", "drand control listen address")
	drandDataDir := fs.String("drand-data-dir", "", "drand data directory (required when --drand-supervise)")

	drandBinary := fs.String("drand-binary", "drand", "path to drand binary")
	drandNoRestart := fs.Bool("drand-no-restart", false, "do not restart drand subprocess after it exits (debugging)")
	drandRestartMin := fs.Duration("drand-restart-backoff-min", 1*time.Second, "minimum delay before restarting drand after exit")
	drandRestartMax := fs.Duration("drand-restart-backoff-max", 30*time.Second, "maximum delay between drand restart attempts")
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

	if *grpcMaxConcurrentStreams > math.MaxUint32 {
		return cliConfig{}, false, fmt.Errorf("%w (value=%d max=%d)", errGRPCMaxConcurrentStreams, *grpcMaxConcurrentStreams, uint64(math.MaxUint32))
	}

	grpcCfg := vrfserver.GRPCServerConfig{
		MaxConnectionIdle:            *grpcMaxConnectionIdle,
		MaxConnectionAge:             *grpcMaxConnectionAge,
		MaxConnectionAgeGrace:        *grpcMaxConnectionAgeGrace,
		KeepaliveTime:                *grpcKeepaliveTime,
		KeepaliveTimeout:             *grpcKeepaliveTimeout,
		KeepaliveMinTime:             *grpcKeepaliveMinTime,
		KeepalivePermitWithoutStream: *grpcKeepalivePermitWithoutStream,
		MaxConcurrentStreams:         uint32(*grpcMaxConcurrentStreams),
		MaxRecvMsgSize:               *grpcMaxRecvMsgSize,
		MaxSendMsgSize:               *grpcMaxSendMsgSize,
	}

	return cliConfig{
		ListenAddr:          *listenAddr,
		AllowPublicBind:     *allowPublic,
		GRPC:                grpcCfg,
		MetricsEnabled:      *metricsEnabled,
		MetricsAddr:         *metricsAddr,
		ChainID:             *chainID,
		DebugHTTPEnabled:    *debugHTTPEnabled,
		DebugHTTPAddr:       *debugHTTPAddr,
		DrandSupervise:      *drandSupervise,
		DrandHTTP:           *drandHTTP,
		DrandAllowNonLoop:   *drandAllowNonLoop,
		DrandPublicAddr:     *drandPublic,
		DrandPrivateAddr:    *drandPrivate,
		DrandControlAddr:    *drandControl,
		DrandDataDir:        *drandDataDir,
		DrandBinary:         *drandBinary,
		DrandNoRestart:      *drandNoRestart,
		DrandRestartMin:     *drandRestartMin,
		DrandRestartMax:     *drandRestartMax,
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
