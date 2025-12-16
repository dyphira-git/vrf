package sidecar

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"

	sidecarmetrics "github.com/vexxvakan/vrf/sidecar/servers/prometheus/metrics"
)

var (
	execCommand = exec.Command
	timeAfter   = time.After

	errInvalidRestartBackoff          = errors.New("invalid drand restart backoff config")
	errDrandDataDirRequired           = errors.New("drand data dir must be provided")
	errDrandPrivateListenAddrRequired = errors.New("drand private listen address must be provided")
	errDrandPublicListenAddrRequired  = errors.New("drand public listen address must be provided")
	errDrandControlListenAddrRequired = errors.New("drand control listen address must be provided")
	errDrandProcessNotStarted         = errors.New("drand process is not started")
)

// DrandProcessConfig configures the supervised drand subprocess.
type DrandProcessConfig struct {
	BinaryPath string
	DataDir    string

	PrivateListen string
	PublicListen  string
	ControlListen string

	ExtraArgs []string

	// DisableRestart disables the automatic restart loop on unexpected exit.
	// Intended for debugging and operator control.
	DisableRestart bool

	// RestartBackoffMin is the minimum delay before attempting a restart.
	// Defaults to 1s when unset.
	RestartBackoffMin time.Duration

	// RestartBackoffMax caps the exponential backoff delay between restarts.
	// Defaults to 30s when unset.
	RestartBackoffMax time.Duration
}

// DrandProcess supervises a local drand daemon process.
type DrandProcess struct {
	cfg     DrandProcessConfig
	logger  *zap.Logger
	metrics sidecarmetrics.Metrics

	ctxDone <-chan struct{}
	cancel  context.CancelFunc

	mu  sync.Mutex
	cmd *exec.Cmd

	restartCount      int
	restartBackoffMin time.Duration
	restartBackoffMax time.Duration

	done chan struct{}
}

func StartDrandProcess(
	parentCtx context.Context,
	cfg DrandProcessConfig,
	logger *zap.Logger,
	m sidecarmetrics.Metrics,
) (*DrandProcess, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	if m == nil {
		m = sidecarmetrics.NewNop()
	}

	if strings.TrimSpace(cfg.DataDir) == "" {
		return nil, errDrandDataDirRequired
	}

	if strings.TrimSpace(cfg.PrivateListen) == "" {
		return nil, errDrandPrivateListenAddrRequired
	}

	if strings.TrimSpace(cfg.PublicListen) == "" {
		return nil, errDrandPublicListenAddrRequired
	}

	if strings.TrimSpace(cfg.ControlListen) == "" {
		return nil, errDrandControlListenAddrRequired
	}

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating drand data dir: %w", err)
	}

	restartMin, restartMax, err := normalizeRestartBackoff(cfg.RestartBackoffMin, cfg.RestartBackoffMax)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(parentCtx)
	p := &DrandProcess{
		cfg: cfg,
		logger: logger.With(
			zap.String("component", "sidecar-drand-process"),
		),
		metrics:           m,
		ctxDone:           ctx.Done(),
		cancel:            cancel,
		restartBackoffMin: restartMin,
		restartBackoffMax: restartMax,
		done:              make(chan struct{}),
	}

	// Ensure drand is started successfully at least once.
	if err := p.startOnce(); err != nil {
		cancel()
		return nil, err
	}

	go p.supervise()
	return p, nil
}

// Stop terminates the supervised drand process and stops further restarts.
func (p *DrandProcess) Stop() {
	if p == nil {
		return
	}

	p.cancel()

	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}

	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-p.done
	}
}

func (p *DrandProcess) supervise() {
	defer close(p.done)

	backoff := p.restartBackoffMin
	for {
		err := p.waitCurrent()
		exitCode, exitCodeOK := exitCodeFromWaitErr(err)

		p.metrics.SetDrandProcessHealthy(false)
		select {
		case <-p.ctxDone:
			return
		default:
		}

		exitFields := []zap.Field{
			zap.Int("restart_count", p.restartCount),
			zap.Error(err),
		}
		if exitCodeOK {
			exitFields = append(exitFields, zap.Int("exit_code", exitCode))
		}
		p.logger.Warn("drand process exited", exitFields...)

		if p.cfg.DisableRestart {
			p.logger.Info(
				"drand restart disabled; not restarting",
				zap.Int("restart_count", p.restartCount),
			)
			return
		}

		p.restartCount++
		restartFields := []zap.Field{
			zap.Int("restart_count", p.restartCount),
			zap.Duration("backoff", backoff),
		}
		if exitCodeOK {
			restartFields = append(restartFields, zap.Int("exit_code", exitCode))
		}
		p.logger.Warn("restarting drand process", restartFields...)

		select {
		case <-timeAfter(backoff):
		case <-p.ctxDone:
			return
		}

		backoff = nextBackoff(backoff, p.restartBackoffMax)

		if err := p.startOnce(); err != nil {
			p.logger.Error(
				"failed to restart drand; retrying",
				zap.Int("restart_count", p.restartCount),
				zap.Error(err),
			)
		}
	}
}

func (p *DrandProcess) startOnce() error {
	bin := strings.TrimSpace(p.cfg.BinaryPath)
	if bin == "" {
		bin = "drand"
	}

	controlPort := drandControlPort(p.cfg.ControlListen)
	if strings.TrimSpace(controlPort) == "" {
		return errDrandControlListenAddrRequired
	}

	args := []string{
		"start",
		"--folder", p.cfg.DataDir,
		"--private-listen", p.cfg.PrivateListen,
		"--public-listen", p.cfg.PublicListen,
		"--control", controlPort,
	}
	args = append(args, p.cfg.ExtraArgs...)

	cmd := execCommand(bin, args...) //nolint:gosec

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("drand stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("drand stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting drand: %w", err)
	}

	p.mu.Lock()
	p.cmd = cmd
	p.mu.Unlock()

	p.metrics.SetDrandProcessHealthy(true)
	p.logger.Info(
		"started drand daemon",
		zap.Int("pid", cmd.Process.Pid),
		zap.Int("restart_count", p.restartCount),
	)

	go p.pipeToLogger(stdout, "stdout")
	go p.pipeToLogger(stderr, "stderr")

	// Ensure the child exits when the sidecar is shutting down.
	go func() {
		<-p.ctxDone
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}()

	return nil
}

func (p *DrandProcess) waitCurrent() error {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()

	if cmd == nil {
		return errDrandProcessNotStarted
	}

	err := cmd.Wait()

	p.mu.Lock()
	if p.cmd == cmd {
		p.cmd = nil
	}
	p.mu.Unlock()

	return err
}

func (p *DrandProcess) pipeToLogger(r io.ReadCloser, stream string) {
	defer func() { _ = r.Close() }()

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		p.logger.Info("drand", zap.String("stream", stream), zap.String("line", line))
	}
}

func drandControlPort(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}

	if host, port, err := net.SplitHostPort(addr); err == nil {
		_ = host
		return port
	}

	return addr
}

func normalizeRestartBackoff(minDelay, maxDelay time.Duration) (time.Duration, time.Duration, error) {
	if minDelay <= 0 {
		minDelay = time.Second
	}
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}

	if maxDelay < minDelay {
		return 0, 0, fmt.Errorf("%w: max must be >= min (min=%s max=%s)", errInvalidRestartBackoff, minDelay, maxDelay)
	}
	return minDelay, maxDelay, nil
}

func nextBackoff(current, maxDelay time.Duration) time.Duration {
	if current >= maxDelay {
		return maxDelay
	}
	next := current * 2
	if next > maxDelay {
		return maxDelay
	}
	return next
}

func exitCodeFromWaitErr(err error) (int, bool) {
	if err == nil {
		return 0, true
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		if code >= 0 {
			return code, true
		}
	}

	return 0, false
}
