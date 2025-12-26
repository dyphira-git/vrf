package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	vrfserver "github.com/vexxvakan/vrf/sidecar/servers/vrf"
)

var (
	errSidecarNonLoopbackBind   = errors.New("refusing to bind sidecar to non-loopback address without allow_public_bind=true in vrf.toml")
	errMetricsNonLoopbackBind   = errors.New("refusing to bind metrics to non-loopback address without allow_public_bind=true in vrf.toml")
	errDebugHTTPNonLoopbackBind = errors.New("refusing to bind debug http to non-loopback address without allow_public_bind=true in vrf.toml")
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

	DrandHTTP          string
	DrandAllowNonLoop  bool
	DrandPublicAddr    string
	DrandPrivateAddr   string
	DrandControlAddr   string
	DrandDataDir       string
	DrandID            string
	DrandBinary        string
	DrandNoRestart     bool
	DrandRestartMin    time.Duration
	DrandRestartMax    time.Duration
	DrandChainHashHex  string
	DrandPublicKeyB64  string
	DrandPeriodSeconds uint
	DrandGenesisUnix   int64
	DKGBeaconID        string
	DKGThreshold       uint
	DKGPeriodSeconds   uint
	DKGGenesisUnix     int64
	DKGTimeout         time.Duration
	DKGCatchupSeconds  uint
	DKGJoinerAddrs     []string
	GroupSourceAddr    string
	GroupSourceToken   string

	ChainGRPCAddr       string
	ChainRPCAddr        string
	ChainTimeoutCommit  time.Duration
	ReshareEnabled      bool
	DrandReshareTimeout time.Duration
	DrandReshareArgs    []string
}

type drandFileConfig struct {
	HTTP                 string   `toml:"http"`
	AllowNonLoopbackHTTP *bool    `toml:"allow_non_loopback_http"`
	AllowPublicBind      *bool    `toml:"allow_public_bind"`
	PublicAddr           string   `toml:"public_addr"`
	PrivateAddr          string   `toml:"private_addr"`
	ControlAddr          string   `toml:"control_addr"`
	DataDir              string   `toml:"data_dir"`
	Binary               string   `toml:"binary"`
	ID                   string   `toml:"id"`
	NoRestart            *bool    `toml:"no_restart"`
	RestartBackoffMin    string   `toml:"restart_backoff_min"`
	RestartBackoffMax    string   `toml:"restart_backoff_max"`
	DKGBeaconID          string   `toml:"dkg_beacon_id"`
	DKGThreshold         uint     `toml:"dkg_threshold"`
	DKGPeriodSeconds     uint     `toml:"dkg_period_seconds"`
	DKGGenesisUnix       int64    `toml:"dkg_genesis_time_unix"`
	DKGTimeout           string   `toml:"dkg_timeout"`
	DKGCatchupSeconds    uint     `toml:"dkg_catchup_period_seconds"`
	DKGJoinerAddrs       []string `toml:"dkg_joiner_addrs"`
	GroupSourceAddr      string   `toml:"group_source_addr"`
	GroupSourceToken     string   `toml:"group_source_token"`
}

func parseFlags(args []string) (cliConfig, error) {
	configPath, configPathSet := findFlagValue(args, "drand-config")
	if !configPathSet {
		configPath = defaultDrandConfigPath()
	}

	fileCfg, err := loadDrandFileConfig(configPath, configPathSet)
	if err != nil {
		return cliConfig{}, err
	}

	fs := flag.NewFlagSet("sidecar", flag.ContinueOnError)

	listenAddr := fs.String("listen-addr", "127.0.0.1:8090", "sidecar gRPC listen address (loopback or UDS via unix://)")
	allowPublic := fileCfg.allowPublicBindOr(false)

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

	_ = fs.String("drand-config", configPath, "path to TOML vrf sidecar config file (optional; defaults to <chain_home>/config/vrf.toml when detectable)")
	drandHTTP := fs.String("drand-http", fileCfg.httpOr(""), "drand HTTP base URL (defaults to http://<drand-public-addr>)")
	drandAllowNonLoop := fs.Bool("drand-allow-non-loopback-http", fileCfg.allowNonLoopbackOr(false), "allow drand-http to point at a non-loopback host (unsafe; intended for containerized dev)")
	drandPublic := fs.String("drand-public-addr", fileCfg.publicAddrOr("127.0.0.1:8081"), "drand public listen address (also used for HTTP)")
	drandPrivate := fs.String("drand-private-addr", fileCfg.privateAddrOr("0.0.0.0:4444"), "drand private listen address")
	drandControl := fs.String("drand-control-addr", fileCfg.controlAddrOr("127.0.0.1:8888"), "drand control listen address")
	drandDataDir := fs.String("drand-data-dir", fileCfg.dataDirOr(""), "drand data directory (required; normally set via vrf.toml as data_dir)")
	drandID := fs.String("drand-id", fileCfg.idOr("default"), "drand beacon ID (default \"default\")")

	drandBinary := fs.String("drand-binary", fileCfg.binaryOr("drand"), "path to drand binary")
	drandNoRestart := fs.Bool("drand-no-restart", fileCfg.noRestartOr(false), "do not restart drand subprocess after it exits (debugging)")
	restartMinDefault, err := fileCfg.restartBackoffMinOr(1 * time.Second)
	if err != nil {
		return cliConfig{}, fmt.Errorf("invalid restart_backoff_min in %q: %w", configPath, err)
	}
	restartMaxDefault, err := fileCfg.restartBackoffMaxOr(30 * time.Second)
	if err != nil {
		return cliConfig{}, fmt.Errorf("invalid restart_backoff_max in %q: %w", configPath, err)
	}
	drandRestartMin := fs.Duration("drand-restart-backoff-min", restartMinDefault, "minimum delay before restarting drand after exit")
	drandRestartMax := fs.Duration("drand-restart-backoff-max", restartMaxDefault, "maximum delay between drand restart attempts")
	chainHashHex := fs.String("drand-chain-hash", "", "expected drand chain hash (hex)")
	publicKeyB64 := fs.String("drand-public-key", "", "expected drand group public key (base64)")
	periodSeconds := fs.Uint("drand-period-seconds", 0, "drand beacon period in seconds")
	genesisUnix := fs.Int64("drand-genesis-unix", 0, "drand genesis time (unix seconds)")

	reshareEnabled := fs.Bool("reshare-enabled", true, "enable drand reshare listener (recommended/required for validator ops)")
	reshareTimeout := fs.Duration("drand-reshare-timeout", 30*time.Minute, "timeout for drand reshare execution")
	var reshareArgs stringSliceFlag
	fs.Var(&reshareArgs, "drand-reshare-arg", "additional arg to pass to drand reshare (repeatable; not implemented yet)")

	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}

	chainHome := resolveChainHome()
	chainRPCAddr, chainGRPCAddr, chainTimeoutCommit, err := resolveChainAddrs(chainHome)
	if err != nil {
		return cliConfig{}, err
	}

	dkgTimeout, err := fileCfg.dkgTimeoutOr(0)
	if err != nil {
		return cliConfig{}, fmt.Errorf("invalid dkg_timeout in %q: %w", configPath, err)
	}

	if *grpcMaxConcurrentStreams > math.MaxUint32 {
		return cliConfig{}, fmt.Errorf("%w (value=%d max=%d)", errGRPCMaxConcurrentStreams, *grpcMaxConcurrentStreams, uint64(math.MaxUint32))
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
		AllowPublicBind:     allowPublic,
		GRPC:                grpcCfg,
		MetricsEnabled:      *metricsEnabled,
		MetricsAddr:         *metricsAddr,
		ChainID:             *chainID,
		DebugHTTPEnabled:    *debugHTTPEnabled,
		DebugHTTPAddr:       *debugHTTPAddr,
		DrandHTTP:           *drandHTTP,
		DrandAllowNonLoop:   *drandAllowNonLoop,
		DrandPublicAddr:     *drandPublic,
		DrandPrivateAddr:    *drandPrivate,
		DrandControlAddr:    *drandControl,
		DrandDataDir:        *drandDataDir,
		DrandID:             *drandID,
		DrandBinary:         *drandBinary,
		DrandNoRestart:      *drandNoRestart,
		DrandRestartMin:     *drandRestartMin,
		DrandRestartMax:     *drandRestartMax,
		DrandChainHashHex:   *chainHashHex,
		DrandPublicKeyB64:   *publicKeyB64,
		DrandPeriodSeconds:  *periodSeconds,
		DrandGenesisUnix:    *genesisUnix,
		DKGBeaconID:         fileCfg.dkgBeaconID(),
		DKGThreshold:        fileCfg.DKGThreshold,
		DKGPeriodSeconds:    fileCfg.DKGPeriodSeconds,
		DKGGenesisUnix:      fileCfg.DKGGenesisUnix,
		DKGTimeout:          dkgTimeout,
		DKGCatchupSeconds:   fileCfg.DKGCatchupSeconds,
		DKGJoinerAddrs:      fileCfg.dkgJoinerAddrs(),
		GroupSourceAddr:     fileCfg.groupSourceAddr(),
		GroupSourceToken:    fileCfg.groupSourceToken(),
		ChainGRPCAddr:       chainGRPCAddr,
		ChainRPCAddr:        chainRPCAddr,
		ChainTimeoutCommit:  chainTimeoutCommit,
		ReshareEnabled:      *reshareEnabled,
		DrandReshareTimeout: *reshareTimeout,
		DrandReshareArgs:    []string(reshareArgs),
	}, nil
}

func loadDrandFileConfig(path string, requireExists bool) (drandFileConfig, error) {
	if strings.TrimSpace(path) == "" {
		return drandFileConfig{}, nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if !requireExists && errors.Is(err, os.ErrNotExist) {
			return drandFileConfig{}, nil
		}
		return drandFileConfig{}, fmt.Errorf("reading --drand-config file %q: %w", path, err)
	}

	var cfg drandFileConfig
	md, err := toml.Decode(string(b), &cfg)
	if err != nil {
		return drandFileConfig{}, fmt.Errorf("parsing --drand-config file %q: %w", path, err)
	}
	if md.IsDefined("supervise") {
		return drandFileConfig{}, fmt.Errorf("unsupported vrf.toml key %q in %q: drand supervision is always enabled; remove the key", "supervise", path)
	}
	if md.IsDefined("version_check") {
		return drandFileConfig{}, fmt.Errorf("unsupported vrf.toml key %q in %q: drand version checks are always enforced; remove the key", "version_check", path)
	}

	return cfg, nil
}

func findFlagValue(args []string, name string) (string, bool) {
	if strings.TrimSpace(name) == "" {
		return "", false
	}

	long := "--" + name
	short := "-" + name
	longEq := long + "="
	shortEq := short + "="

	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case strings.HasPrefix(arg, longEq):
			return strings.TrimPrefix(arg, longEq), true
		case strings.HasPrefix(arg, shortEq):
			return strings.TrimPrefix(arg, shortEq), true
		case arg == long || arg == short:
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
	}

	return "", false
}

func (c drandFileConfig) allowNonLoopbackOr(v bool) bool {
	if c.AllowNonLoopbackHTTP == nil {
		return v
	}
	return *c.AllowNonLoopbackHTTP
}

func (c drandFileConfig) allowPublicBindOr(v bool) bool {
	if c.AllowPublicBind == nil {
		return v
	}
	return *c.AllowPublicBind
}

func (c drandFileConfig) httpOr(v string) string {
	if strings.TrimSpace(c.HTTP) == "" {
		return v
	}
	return strings.TrimSpace(c.HTTP)
}

func defaultDrandConfigPath() string {
	chainHome := resolveChainHome()
	if chainHome == "" {
		return ""
	}

	return filepath.Join(chainHome, "config", "vrf.toml")
}

func resolveChainHome() string {
	chainHome := strings.TrimSpace(firstNonEmptyEnv("CHAIN_DIR", "CHAIN_HOME", "DAEMON_HOME"))

	if chainHome == "" && dirExists("/.vrf/.chaind") {
		chainHome = "/.vrf/.chaind"
	}

	if chainHome == "" {
		if userHome, err := os.UserHomeDir(); err == nil && strings.TrimSpace(userHome) != "" {
			chainHome = filepath.Join(userHome, ".chaind")
		}
	}
	if chainHome == "" {
		chainHome = detectRunningChaindHome()
	}

	return chainHome
}

func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func detectRunningChaindHome() string {
	const binaryName = "chaind"

	if runtime.GOOS == "linux" {
		if home := detectChainHomeFromProc(binaryName); home != "" {
			return home
		}
	}

	return detectChainHomeFromPS(binaryName)
}

func detectChainHomeFromProc(binaryName string) string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}

	for _, ent := range entries {
		if !ent.IsDir() || !isAllDigits(ent.Name()) {
			continue
		}

		cmdlinePath := filepath.Join("/proc", ent.Name(), "cmdline")
		b, err := os.ReadFile(cmdlinePath)
		if err != nil || len(b) == 0 {
			continue
		}

		args := splitNullSeparated(b)
		if len(args) == 0 {
			continue
		}

		if filepath.Base(args[0]) != binaryName || !containsArg(args, "start") {
			continue
		}

		if home := parseHomeFlag(args); home != "" {
			return home
		}

		if userHome, err := os.UserHomeDir(); err == nil && strings.TrimSpace(userHome) != "" {
			return filepath.Join(userHome, "."+binaryName)
		}

		return ""
	}

	return ""
}

func detectChainHomeFromPS(binaryName string) string {
	out, err := exec.Command("ps", "axww", "-o", "command=").Output()
	if err != nil {
		out, err = exec.Command("ps", "ax", "-o", "command=").Output()
		if err != nil {
			return ""
		}
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) == 0 || filepath.Base(fields[0]) != binaryName || !containsArg(fields, "start") {
			continue
		}

		if home := parseHomeFlag(fields); home != "" {
			return home
		}

		if userHome, err := os.UserHomeDir(); err == nil && strings.TrimSpace(userHome) != "" {
			return filepath.Join(userHome, "."+binaryName)
		}

		return ""
	}

	return ""
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func splitNullSeparated(b []byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] != 0 {
			continue
		}
		if i > start {
			out = append(out, string(b[start:i]))
		}
		start = i + 1
	}
	if start < len(b) {
		out = append(out, string(b[start:]))
	}
	return out
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func parseHomeFlag(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--home" && i+1 < len(args) {
			return strings.TrimSpace(args[i+1])
		}
		if strings.HasPrefix(args[i], "--home=") {
			return strings.TrimSpace(strings.TrimPrefix(args[i], "--home="))
		}
	}
	return ""
}

func (c drandFileConfig) publicAddrOr(v string) string {
	if strings.TrimSpace(c.PublicAddr) == "" {
		return v
	}
	return strings.TrimSpace(c.PublicAddr)
}

func (c drandFileConfig) privateAddrOr(v string) string {
	if strings.TrimSpace(c.PrivateAddr) == "" {
		return v
	}
	return strings.TrimSpace(c.PrivateAddr)
}

func (c drandFileConfig) controlAddrOr(v string) string {
	if strings.TrimSpace(c.ControlAddr) == "" {
		return v
	}
	return strings.TrimSpace(c.ControlAddr)
}

func (c drandFileConfig) dataDirOr(v string) string {
	if strings.TrimSpace(c.DataDir) == "" {
		return v
	}
	return strings.TrimSpace(c.DataDir)
}

func (c drandFileConfig) binaryOr(v string) string {
	if strings.TrimSpace(c.Binary) == "" {
		return v
	}
	return strings.TrimSpace(c.Binary)
}

func (c drandFileConfig) idOr(v string) string {
	if strings.TrimSpace(c.ID) == "" {
		return v
	}
	return strings.TrimSpace(c.ID)
}

func (c drandFileConfig) noRestartOr(v bool) bool {
	if c.NoRestart == nil {
		return v
	}
	return *c.NoRestart
}

func (c drandFileConfig) restartBackoffMinOr(v time.Duration) (time.Duration, error) {
	s := strings.TrimSpace(c.RestartBackoffMin)
	if s == "" {
		return v, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	return d, nil
}

func (c drandFileConfig) restartBackoffMaxOr(v time.Duration) (time.Duration, error) {
	s := strings.TrimSpace(c.RestartBackoffMax)
	if s == "" {
		return v, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	return d, nil
}

type clientTomlConfig struct {
	Node     string `toml:"node"`
	GRPCAddr string `toml:"grpc-addr"`
}

type appTomlConfig struct {
	GRPC struct {
		Address string `toml:"address"`
	} `toml:"grpc"`
}

type cometTomlConfig struct {
	RPC struct {
		Laddr string `toml:"laddr"`
	} `toml:"rpc"`
	Consensus struct {
		TimeoutCommit string `toml:"timeout_commit"`
	} `toml:"consensus"`
}

func resolveChainAddrs(chainHome string) (string, string, time.Duration, error) {
	chainHome = strings.TrimSpace(chainHome)
	if chainHome == "" {
		return "", "", 0, fmt.Errorf("unable to determine chain home (set CHAIN_HOME, CHAIN_DIR, or DAEMON_HOME)")
	}

	cfgDir := filepath.Join(chainHome, "config")
	clientPath := filepath.Join(cfgDir, "client.toml")
	appPath := filepath.Join(cfgDir, "app.toml")
	cometPath := filepath.Join(cfgDir, "config.toml")

	var clientCfg clientTomlConfig
	if err := decodeTomlFile(clientPath, &clientCfg); err != nil {
		return "", "", 0, err
	}

	var appCfg appTomlConfig
	if err := decodeTomlFile(appPath, &appCfg); err != nil {
		return "", "", 0, err
	}

	var cometCfg cometTomlConfig
	if err := decodeTomlFile(cometPath, &cometCfg); err != nil {
		return "", "", 0, err
	}

	rpcAddr := strings.TrimSpace(clientCfg.Node)
	if rpcAddr == "" {
		rpcAddr = strings.TrimSpace(cometCfg.RPC.Laddr)
	}

	grpcAddr := strings.TrimSpace(clientCfg.GRPCAddr)
	if grpcAddr == "" {
		grpcAddr = strings.TrimSpace(appCfg.GRPC.Address)
	}

	if rpcAddr == "" {
		return "", "", 0, fmt.Errorf("chain RPC address missing (set config/client.toml node or config/config.toml rpc.laddr)")
	}
	if grpcAddr == "" {
		return "", "", 0, fmt.Errorf("chain gRPC address missing (set config/client.toml grpc-addr or config/app.toml grpc.address)")
	}

	normalizedRPC, err := normalizeCometRPCAddr(rpcAddr)
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid chain RPC address %q: %w", rpcAddr, err)
	}

	normalizedGRPC, err := normalizeGRPCAddr(grpcAddr)
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid chain gRPC address %q: %w", grpcAddr, err)
	}

	timeoutCommit, err := parseTimeoutCommit(cometCfg.Consensus.TimeoutCommit)
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid consensus.timeout_commit in config.toml: %w", err)
	}

	return normalizedRPC, normalizedGRPC, timeoutCommit, nil
}

func parseTimeoutCommit(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	return time.ParseDuration(raw)
}

func decodeTomlFile(path string, dest any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("reading %q: %w", path, err)
	}

	if _, err := toml.Decode(string(b), dest); err != nil {
		return fmt.Errorf("parsing %q: %w", path, err)
	}
	return nil
}

func normalizeCometRPCAddr(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", fmt.Errorf("empty address")
	}

	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil {
			return "", err
		}
		scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
		host := strings.TrimSpace(u.Host)
		if host == "" {
			host = strings.TrimSpace(u.Path)
		}
		host = normalizeHostPort(host)

		switch scheme {
		case "http", "https":
			return scheme + "://" + host, nil
		case "tcp", "ws", "wss":
			return "http://" + host, nil
		default:
			return "", fmt.Errorf("unsupported scheme %q", scheme)
		}
	}

	return "http://" + normalizeHostPort(addr), nil
}

func normalizeGRPCAddr(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", fmt.Errorf("empty address")
	}

	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil {
			return "", err
		}
		host := strings.TrimSpace(u.Host)
		if host == "" {
			host = strings.TrimSpace(u.Path)
		}
		return normalizeHostPort(host), nil
	}

	return normalizeHostPort(addr), nil
}

func normalizeHostPort(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}

	switch host {
	case "0.0.0.0", "::":
		host = "127.0.0.1"
	}

	return net.JoinHostPort(host, port)
}

func (c drandFileConfig) dkgBeaconID() string {
	return strings.TrimSpace(c.DKGBeaconID)
}

func (c drandFileConfig) dkgTimeoutOr(v time.Duration) (time.Duration, error) {
	s := strings.TrimSpace(c.DKGTimeout)
	if s == "" {
		return v, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	return d, nil
}

func (c drandFileConfig) dkgJoinerAddrs() []string {
	if len(c.DKGJoinerAddrs) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.DKGJoinerAddrs))
	for _, addr := range c.DKGJoinerAddrs {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		out = append(out, addr)
	}
	return out
}

func (c drandFileConfig) groupSourceAddr() string {
	return strings.TrimSpace(c.GroupSourceAddr)
}

func (c drandFileConfig) groupSourceToken() string {
	return strings.TrimSpace(c.GroupSourceToken)
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
