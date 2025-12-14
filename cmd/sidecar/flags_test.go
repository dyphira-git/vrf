package main

import "testing"

func TestParseFlags_DebugHTTPDisabledByDefault(t *testing.T) {
	t.Parallel()

	cfg, showVersion, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags returned error: %v", err)
	}
	if showVersion {
		t.Fatalf("expected showVersion=false by default")
	}
	if cfg.DebugHTTPEnabled {
		t.Fatalf("expected DebugHTTPEnabled=false by default")
	}
	if cfg.DebugHTTPAddr == "" {
		t.Fatalf("expected DebugHTTPAddr to have a default value")
	}
	if !isLoopbackAddr(cfg.DebugHTTPAddr) {
		t.Fatalf("expected default DebugHTTPAddr to be loopback/UDS, got %q", cfg.DebugHTTPAddr)
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
		t.Fatalf("expected error for non-loopback debug http bind without AllowPublicBind")
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
