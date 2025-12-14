package vrf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/vexxvakan/vrf/sidecar"
	sidecarmetrics "github.com/vexxvakan/vrf/sidecar/servers/prometheus/metrics"
	vrftypes "github.com/vexxvakan/vrf/sidecar/servers/vrf/types"
)

type stubService struct{}

func (stubService) Randomness(context.Context, uint64) (*vrftypes.QueryRandomnessResponse, error) {
	return &vrftypes.QueryRandomnessResponse{
		DrandRound:        1,
		Randomness:        []byte{0x01, 0x02},
		Signature:         []byte{0x03, 0x04},
		PreviousSignature: []byte{0x05, 0x06},
	}, nil
}

func (stubService) Info(context.Context) (*vrftypes.QueryInfoResponse, error) {
	return &vrftypes.QueryInfoResponse{
		ChainHash:      []byte{0xaa, 0xbb},
		PublicKey:      []byte{0xcc, 0xdd},
		PeriodSeconds:  42,
		GenesisUnixSec: 123,
	}, nil
}

func TestStartDebugHTTP_UnixSocketServesInfo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Keep the socket path short to avoid OS limits (notably on macOS).
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("vrf_debug_http_%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(socketPath) })

	server := NewServer(sidecar.Service(stubService{}), zap.NewNop(), sidecarmetrics.NewNop())

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.StartDebugHTTP(ctx, "unix://"+socketPath)
	}()

	waitForDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(waitForDeadline) {
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   2 * time.Second,
	}

	resp, err := client.Get("http://unix/vrf/v1/info")
	if err != nil {
		t.Fatalf("GET /vrf/v1/info failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var decoded map[string]any
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response json: %v (body=%q)", err, string(body))
	}

	periodValue, ok := decoded["periodSeconds"]
	if !ok {
		periodValue = decoded["period_seconds"]
	}
	switch v := periodValue.(type) {
	case float64:
		if v != float64(42) {
			t.Fatalf("expected periodSeconds=42, got %v (body=%q)", periodValue, string(body))
		}
	case string:
		if v != "42" {
			t.Fatalf("expected periodSeconds=42, got %v (body=%q)", periodValue, string(body))
		}
	default:
		t.Fatalf("expected periodSeconds=42, got %T=%v (body=%q)", periodValue, periodValue, string(body))
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("StartDebugHTTP returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for StartDebugHTTP to exit")
	}
}
