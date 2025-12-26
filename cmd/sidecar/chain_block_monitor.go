package main

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/vexxvakan/vrf/sidecar/chainws"
)

const chainBlockTimeoutGrace = 100 * time.Millisecond

type chainBlockMonitor struct {
	logger     *zap.Logger
	timeout    time.Duration
	drandCtl   *drandController
	seenBlock  bool
	paused     bool
	lastTime   time.Time
	lastHeight int64
	mu         sync.Mutex
}

func newChainBlockMonitor(logger *zap.Logger, timeout time.Duration, drandCtl *drandController) *chainBlockMonitor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &chainBlockMonitor{
		logger:   logger.With(zap.String("component", "sidecar-chain-blocks")),
		timeout:  timeout,
		drandCtl: drandCtl,
	}
}

func (m *chainBlockMonitor) OnNewBlock(ctx context.Context, evt chainws.NewBlockHeaderEvent) {
	if m == nil {
		return
	}

	blockTime := evt.Time
	if blockTime.IsZero() {
		blockTime = time.Now()
	}

	m.mu.Lock()
	m.seenBlock = true
	m.lastTime = blockTime
	m.lastHeight = evt.Height
	paused := m.paused
	m.mu.Unlock()

	if !paused {
		return
	}

	if m.drandCtl == nil {
		m.mu.Lock()
		m.paused = false
		m.mu.Unlock()
		return
	}

	if err := m.drandCtl.Start(ctx); err != nil {
		if ctx.Err() != nil {
			return
		}
		m.logger.Warn("failed to restart drand after chain resumed", zap.Error(err))
		return
	}

	m.mu.Lock()
	m.paused = false
	m.mu.Unlock()

	m.logger.Info(
		"chain resumed",
		zap.Int64("height", evt.Height),
		zap.Time("block_time", blockTime),
	)
}

func (m *chainBlockMonitor) Run(ctx context.Context) {
	if m == nil || m.timeout <= 0 {
		return
	}

	poll := chainBlockPollInterval(m.timeout)
	m.logger.Info(
		"chain block monitor enabled",
		zap.Duration("timeout", m.timeout),
		zap.Duration("interval", poll),
	)

	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		m.mu.Lock()
		if !m.seenBlock || m.paused {
			m.mu.Unlock()
			continue
		}
		lastTime := m.lastTime
		lastHeight := m.lastHeight
		m.mu.Unlock()

		if lastTime.IsZero() {
			continue
		}

		since := time.Since(lastTime)
		if since < 0 || since <= m.timeout {
			continue
		}

		if m.drandCtl != nil {
			if err := m.drandCtl.Start(ctx); err != nil && ctx.Err() == nil {
				m.logger.Warn("failed to ensure drand is running after chain stall", zap.Error(err))
			}
		}

		m.mu.Lock()
		m.paused = true
		m.mu.Unlock()

		m.logger.Info(
			"chain stalled; leaving drand running",
			zap.Int64("height", lastHeight),
			zap.Time("last_block_time", lastTime),
			zap.Duration("since_last_block", since),
		)
	}
}

func chainBlockPollInterval(timeout time.Duration) time.Duration {
	poll := timeout / 4
	if poll < 200*time.Millisecond {
		poll = 200 * time.Millisecond
	}
	if poll > 5*time.Second {
		poll = 5 * time.Second
	}
	return poll
}
