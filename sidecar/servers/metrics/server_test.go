package metrics_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/vexxvakan/vrf/sidecar/servers/metrics"
)

// Test that Starting the server fails if the address is incorrect.
func TestStart(t *testing.T) {
	t.Run("Start fails with incorrect address", func(t *testing.T) {
		address := ":8081"

		ps, err := metrics.NewPrometheusServer(address, nil)
		require.Nil(t, ps)
		require.Error(t, err, "invalid prometheus server address: :8081")
	})

	t.Run("Start succeeds with correct address", func(t *testing.T) {
		address := freeTCPAddress(t)

		ps, err := metrics.NewPrometheusServer(address, zap.NewNop())
		require.NotNil(t, ps)
		require.NoError(t, err)

		// start the server
		go ps.Start()

		// ping the server
		require.Eventually(t, func() bool {
			return pingServer("http://" + address + "/metrics")
		}, 3*time.Second, 50*time.Millisecond)

		// close the server
		ps.Close()

		// expect the server to be closed within 3 seconds
		select {
		case <-ps.Done():
		case <-time.After(3 * time.Second):
		}
	})

	t.Run("Start succeeds with unix socket address", func(t *testing.T) {
		dir, err := os.MkdirTemp("/tmp", "vrf-metrics-")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		socketPath := filepath.Join(dir, "vrf-metrics.sock")
		address := "unix://" + socketPath

		ps, err := metrics.NewPrometheusServer(address, zap.NewNop())
		require.NotNil(t, ps)
		require.NoError(t, err)

		go ps.Start()

		require.Eventually(t, func() bool {
			return pingUnixServer(socketPath, "/metrics")
		}, 3*time.Second, 50*time.Millisecond)

		ps.Close()

		// expect the server to be closed within 3 seconds
		select {
		case <-ps.Done():
		case <-time.After(3 * time.Second):
		}

		_, statErr := os.Stat(socketPath)
		require.ErrorIs(t, statErr, os.ErrNotExist)
	})

	t.Run("Start removes stale unix socket before binding", func(t *testing.T) {
		dir, err := os.MkdirTemp("/tmp", "vrf-metrics-")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		socketPath := filepath.Join(dir, "vrf-metrics.sock")
		address := "unix://" + socketPath

		err = os.WriteFile(socketPath, []byte("stale"), 0o600)
		require.NoError(t, err)

		_, statErr := os.Stat(socketPath)
		require.NoError(t, statErr)

		ps, err := metrics.NewPrometheusServer(address, zap.NewNop())
		require.NotNil(t, ps)
		require.NoError(t, err)

		go ps.Start()

		require.Eventually(t, func() bool {
			return pingUnixServer(socketPath, "/metrics")
		}, 3*time.Second, 50*time.Millisecond)

		ps.Close()

		// expect the server to be closed within 3 seconds
		select {
		case <-ps.Done():
		case <-time.After(3 * time.Second):
		}

		_, finalStatErr := os.Stat(socketPath)
		require.ErrorIs(t, finalStatErr, os.ErrNotExist)
	})
}

func freeTCPAddress(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := ln.Addr().String()
	require.NoError(t, ln.Close())
	return address
}

func pingServer(address string) bool {
	timeout := 5 * time.Second
	client := http.Client{
		Timeout: timeout,
	}

	resp, err := client.Get(address)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	return hasMetricsBody(resp)
}

func pingUnixServer(socketPath string, urlPath string) bool {
	timeout := 5 * time.Second
	dialer := &net.Dialer{}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	client := http.Client{
		Timeout:   timeout,
		Transport: transport,
	}

	resp, err := client.Get("http://unix" + urlPath)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	return hasMetricsBody(resp)
}

func hasMetricsBody(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}
	return bytes.Contains(body, []byte("go_goroutines")) ||
		bytes.Contains(body, []byte("process_cpu_seconds_total"))
}
