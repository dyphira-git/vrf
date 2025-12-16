package vrf

import (
	"context"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/vexxvakan/vrf/sidecar"
	sidecarmetrics "github.com/vexxvakan/vrf/sidecar/servers/prometheus/metrics"
)

func TestGRPCServerConfig_ServerOptions_UnsetByDefault(t *testing.T) {
	t.Parallel()

	opts, err := (GRPCServerConfig{}).serverOptions()
	if err != nil {
		t.Fatalf("serverOptions returned error: %v", err)
	}
	if len(opts) != 0 {
		t.Fatalf("expected no grpc.ServerOption by default, got %d", len(opts))
	}
}

func TestGRPCServerConfig_KeepaliveParams_DefaultsAppliedWhenAnyFieldIsSet(t *testing.T) {
	t.Parallel()

	cfg := GRPCServerConfig{
		MaxConnectionAge: time.Hour,
	}

	kp, ok := cfg.keepaliveParams()
	if !ok {
		t.Fatal("expected keepalive params to be applied")
	}
	if kp.MaxConnectionAge != time.Hour {
		t.Fatalf("expected MaxConnectionAge=1h, got %v", kp.MaxConnectionAge)
	}
	if kp.Time != 2*time.Hour {
		t.Fatalf("expected default Time=2h, got %v", kp.Time)
	}
	if kp.Timeout != 20*time.Second {
		t.Fatalf("expected default Timeout=20s, got %v", kp.Timeout)
	}
}

func TestGRPCServerConfig_EnforcementPolicy_DefaultsAppliedWhenPermitWithoutStreamIsTrue(t *testing.T) {
	t.Parallel()

	cfg := GRPCServerConfig{
		KeepalivePermitWithoutStream: true,
	}

	ep, ok := cfg.enforcementPolicy()
	if !ok {
		t.Fatal("expected enforcement policy to be applied")
	}
	if ep.MinTime != 5*time.Minute {
		t.Fatalf("expected default MinTime=5m, got %v", ep.MinTime)
	}
	if !ep.PermitWithoutStream {
		t.Fatal("expected PermitWithoutStream=true")
	}
}

func TestGRPCServerConfig_Validate_NegativeValuesRejected(t *testing.T) {
	t.Parallel()

	cfg := GRPCServerConfig{
		KeepaliveTimeout: -1 * time.Second,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected Validate error for negative duration")
	}
}

func TestServer_StartWithListener_PassesHardeningOptionsToGRPCServer(t *testing.T) {
	t.Parallel()

	server := NewServer(sidecar.Service(stubService{}), zap.NewNop(), sidecarmetrics.NewNop())
	if err := server.SetGRPCConfig(GRPCServerConfig{MaxConcurrentStreams: 10, MaxRecvMsgSize: 1024}); err != nil {
		t.Fatalf("SetGRPCConfig returned error: %v", err)
	}

	var got int
	server.newGRPC = func(opts ...grpc.ServerOption) *grpc.Server {
		got = len(opts)
		return grpc.NewServer(opts...)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen failed: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := server.StartWithListener(ctx, ln); err != nil {
		t.Fatalf("StartWithListener returned error: %v", err)
	}
	if got == 0 {
		t.Fatal("expected grpc.ServerOption(s) to be passed to grpc.NewServer")
	}
}
