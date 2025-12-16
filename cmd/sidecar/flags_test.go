package main

import "testing"

func TestParseFlags_DebugHTTPDisabledByDefault(t *testing.T) {
	t.Parallel()

	cfg, showVersion, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags returned error: %v", err)
	}
	if showVersion {
		t.Fatal("expected showVersion=false by default")
	}
	if cfg.DebugHTTPEnabled {
		t.Fatal("expected DebugHTTPEnabled=false by default")
	}
	if cfg.DebugHTTPAddr == "" {
		t.Fatal("expected DebugHTTPAddr to have a default value")
	}
	if !isLoopbackAddr(cfg.DebugHTTPAddr) {
		t.Fatalf("expected default DebugHTTPAddr to be loopback/UDS, got %q", cfg.DebugHTTPAddr)
	}

	if cfg.GRPC.MaxConnectionIdle != 0 ||
		cfg.GRPC.MaxConnectionAge != 0 ||
		cfg.GRPC.MaxConnectionAgeGrace != 0 ||
		cfg.GRPC.KeepaliveTime != 0 ||
		cfg.GRPC.KeepaliveTimeout != 0 ||
		cfg.GRPC.KeepaliveMinTime != 0 ||
		cfg.GRPC.KeepalivePermitWithoutStream ||
		cfg.GRPC.MaxConcurrentStreams != 0 ||
		cfg.GRPC.MaxRecvMsgSize != 0 ||
		cfg.GRPC.MaxSendMsgSize != 0 {
		t.Fatalf("expected gRPC hardening config to be unset by default, got %+v", cfg.GRPC)
	}
}

func TestValidateBindConfig_DebugHTTPRequiresAllowPublicForNonLoopback(t *testing.T) {
	t.Parallel()

	cfg := cliConfig{
		ListenAddr:       "127.0.0.1:0",
		MetricsEnabled:   false,
		DebugHTTPEnabled: true,
		DebugHTTPAddr:    "0.0.0.0:8092",
		AllowPublicBind:  false,
	}

	if err := validateBindConfig(cfg); err == nil {
		t.Fatal("expected error for non-loopback debug http bind without AllowPublicBind")
	}

	cfg.AllowPublicBind = true
	if err := validateBindConfig(cfg); err != nil {
		t.Fatalf("expected allow-public to permit non-loopback debug http bind, got: %v", err)
	}
}

func TestValidateBindConfig_DebugHTTPAllowsUnixSocket(t *testing.T) {
	t.Parallel()

	cfg := cliConfig{
		ListenAddr:       "127.0.0.1:0",
		MetricsEnabled:   false,
		DebugHTTPEnabled: true,
		DebugHTTPAddr:    "unix:///tmp/vrf_debug_http_test.sock",
		AllowPublicBind:  false,
	}

	if err := validateBindConfig(cfg); err != nil {
		t.Fatalf("expected unix socket to be allowed, got: %v", err)
	}
}

func TestParseFlags_GRPCServerConfigIsParsed(t *testing.T) {
	t.Parallel()

	cfg, _, err := parseFlags([]string{
		"--grpc-keepalive-time=30s",
		"--grpc-keepalive-timeout=10s",
		"--grpc-keepalive-min-time=15s",
		"--grpc-keepalive-permit-without-stream=true",
		"--grpc-max-connection-idle=1m",
		"--grpc-max-connection-age=2m",
		"--grpc-max-connection-age-grace=3m",
		"--grpc-max-concurrent-streams=123",
		"--grpc-max-recv-msg-size=1048576",
		"--grpc-max-send-msg-size=2097152",
	})
	if err != nil {
		t.Fatalf("parseFlags returned error: %v", err)
	}

	if cfg.GRPC.KeepaliveTime.String() != "30s" {
		t.Fatalf("expected KeepaliveTime=30s, got %v", cfg.GRPC.KeepaliveTime)
	}
	if cfg.GRPC.KeepaliveTimeout.String() != "10s" {
		t.Fatalf("expected KeepaliveTimeout=10s, got %v", cfg.GRPC.KeepaliveTimeout)
	}
	if cfg.GRPC.KeepaliveMinTime.String() != "15s" {
		t.Fatalf("expected KeepaliveMinTime=15s, got %v", cfg.GRPC.KeepaliveMinTime)
	}
	if !cfg.GRPC.KeepalivePermitWithoutStream {
		t.Fatal("expected KeepalivePermitWithoutStream=true")
	}
	if cfg.GRPC.MaxConnectionIdle.String() != "1m0s" {
		t.Fatalf("expected MaxConnectionIdle=1m0s, got %v", cfg.GRPC.MaxConnectionIdle)
	}
	if cfg.GRPC.MaxConnectionAge.String() != "2m0s" {
		t.Fatalf("expected MaxConnectionAge=2m0s, got %v", cfg.GRPC.MaxConnectionAge)
	}
	if cfg.GRPC.MaxConnectionAgeGrace.String() != "3m0s" {
		t.Fatalf("expected MaxConnectionAgeGrace=3m0s, got %v", cfg.GRPC.MaxConnectionAgeGrace)
	}
	if cfg.GRPC.MaxConcurrentStreams != 123 {
		t.Fatalf("expected MaxConcurrentStreams=123, got %v", cfg.GRPC.MaxConcurrentStreams)
	}
	if cfg.GRPC.MaxRecvMsgSize != 1048576 {
		t.Fatalf("expected MaxRecvMsgSize=1048576, got %v", cfg.GRPC.MaxRecvMsgSize)
	}
	if cfg.GRPC.MaxSendMsgSize != 2097152 {
		t.Fatalf("expected MaxSendMsgSize=2097152, got %v", cfg.GRPC.MaxSendMsgSize)
	}

	if err := cfg.GRPC.Validate(); err != nil {
		t.Fatalf("expected gRPC config to validate, got: %v", err)
	}
}

func TestParseFlags_GRPCMaxConcurrentStreamsMustFitUint32(t *testing.T) {
	t.Parallel()

	_, _, err := parseFlags([]string{
		"--grpc-max-concurrent-streams=4294967296",
	})
	if err == nil {
		t.Fatal("expected error when --grpc-max-concurrent-streams exceeds uint32 range")
	}
}
