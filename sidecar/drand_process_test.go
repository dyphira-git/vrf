package sidecar

import (
	"context"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNormalizeRestartBackoff(t *testing.T) {
	minDelay, maxDelay, err := normalizeRestartBackoff(0, 0)
	require.NoError(t, err)
	require.Equal(t, 1*time.Second, minDelay)
	require.Equal(t, 30*time.Second, maxDelay)

	minDelay, maxDelay, err = normalizeRestartBackoff(2*time.Second, 0)
	require.NoError(t, err)
	require.Equal(t, 2*time.Second, minDelay)
	require.Equal(t, 30*time.Second, maxDelay)

	minDelay, maxDelay, err = normalizeRestartBackoff(0, 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, 1*time.Second, minDelay)
	require.Equal(t, 5*time.Second, maxDelay)

	_, _, err = normalizeRestartBackoff(5*time.Second, 2*time.Second)
	require.Error(t, err)
}

func TestNextBackoff(t *testing.T) {
	require.Equal(t, 2*time.Second, nextBackoff(1*time.Second, 30*time.Second))
	require.Equal(t, 30*time.Second, nextBackoff(20*time.Second, 30*time.Second))
	require.Equal(t, 30*time.Second, nextBackoff(30*time.Second, 30*time.Second))
	require.Equal(t, 30*time.Second, nextBackoff(40*time.Second, 30*time.Second))
}

func TestDrandProcess_NoRestart(t *testing.T) {
	origExecCommand := execCommand
	t.Cleanup(func() { execCommand = origExecCommand })

	var startCalls atomic.Int64
	execCommand = func(string, ...string) *exec.Cmd {
		startCalls.Add(1)
		return exec.Command("go", "version")
	}

	p, err := StartDrandProcess(context.Background(), DrandProcessConfig{
		DataDir:           t.TempDir(),
		PrivateListen:     "127.0.0.1:4444",
		PublicListen:      "127.0.0.1:8081",
		ControlListen:     "127.0.0.1:8881",
		DisableRestart:    true,
		RestartBackoffMin: 1 * time.Millisecond,
		RestartBackoffMax: 2 * time.Millisecond,
	}, nil, nil)
	require.NoError(t, err)
	t.Cleanup(p.Stop)

	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for supervisor to exit")
	}

	require.Equal(t, int64(1), startCalls.Load())
	require.Equal(t, 0, p.restartCount)
}

func TestDrandProcess_BackoffSequence(t *testing.T) {
	origExecCommand := execCommand
	origTimeAfter := timeAfter
	t.Cleanup(func() {
		execCommand = origExecCommand
		timeAfter = origTimeAfter
	})

	execCommand = func(string, ...string) *exec.Cmd {
		return exec.Command("go", "version")
	}

	type afterCall struct {
		d  time.Duration
		ch chan time.Time
	}
	afterCalls := make(chan afterCall, 10)
	timeAfter = func(d time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		afterCalls <- afterCall{d: d, ch: ch}
		return ch
	}

	p, err := StartDrandProcess(context.Background(), DrandProcessConfig{
		DataDir:           t.TempDir(),
		PrivateListen:     "127.0.0.1:4444",
		PublicListen:      "127.0.0.1:8081",
		ControlListen:     "127.0.0.1:8881",
		RestartBackoffMin: 10 * time.Millisecond,
		RestartBackoffMax: 25 * time.Millisecond,
	}, nil, nil)
	require.NoError(t, err)
	t.Cleanup(p.Stop)

	want := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		25 * time.Millisecond,
	}

	for i, w := range want {
		select {
		case call := <-afterCalls:
			require.Equal(t, w, call.d)
			if i < len(want)-1 {
				call.ch <- time.Time{}
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for timeAfter call %d", i+1)
		}
	}

	p.Stop()
}
