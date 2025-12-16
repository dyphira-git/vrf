package vrf

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/vexxvakan/vrf/sidecar"
	sidecarmetrics "github.com/vexxvakan/vrf/sidecar/servers/prometheus/metrics"
	"github.com/vexxvakan/vrf/sidecar/servers/vrf/types"
)

const (
	defaultMaxConcurrent = 64
	defaultRatePerSecond = 100
	defaultRateBurst     = 200

	// defaultPerClientLimiterCacheSize bounds memory usage for per-client limiters.
	// It should be comfortably above the expected number of distinct peers.
	defaultPerClientLimiterCacheSize = 1024
)

type Server struct {
	types.VrfServer

	svc sidecar.Service

	grpcSrv  *grpc.Server
	grpcOpts []grpc.ServerOption
	newGRPC  func(opts ...grpc.ServerOption) *grpc.Server
	logger   *zap.Logger
	metrics  sidecarmetrics.Metrics

	sem     chan struct{}
	limiter *rate.Limiter

	perClientMu       sync.Mutex
	perClientLimiters *lru.Cache[string, *rate.Limiter]
	perClientRate     rate.Limit
	perClientBurst    int
}

func NewServer(svc sidecar.Service, logger *zap.Logger, m sidecarmetrics.Metrics) *Server {
	if logger == nil {
		logger = zap.NewNop()
	}

	if m == nil {
		m = sidecarmetrics.NewNop()
	}

	perClientLimiters, err := lru.New[string, *rate.Limiter](defaultPerClientLimiterCacheSize)
	if err != nil {
		logger.Warn(
			"failed to initialize per-client rate limiter cache; falling back to global-only limiting",
			zap.Error(err),
		)
	}

	return &Server{
		svc:     svc,
		logger:  logger.With(zap.String("server", "vrf")),
		metrics: m,
		newGRPC: grpc.NewServer,
		sem:     make(chan struct{}, defaultMaxConcurrent),
		limiter: rate.NewLimiter(
			defaultRatePerSecond,
			defaultRateBurst,
		),
		perClientLimiters: perClientLimiters,
		perClientRate:     defaultRatePerSecond / 2,
		perClientBurst:    defaultRateBurst / 2,
	}
}

func (s *Server) SetGRPCConfig(cfg GRPCServerConfig) error {
	grpcOpts, err := cfg.serverOptions()
	if err != nil {
		return err
	}

	s.grpcOpts = grpcOpts
	return nil
}

func (s *Server) StartWithListener(ctx context.Context, ln net.Listener) error {
	if s.svc == nil {
		return errVrfServiceNil
	}

	newGRPC := s.newGRPC
	if newGRPC == nil {
		newGRPC = grpc.NewServer
	}

	s.grpcSrv = newGRPC(s.grpcOpts...)
	types.RegisterVrfServer(s.grpcSrv, s)

	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		<-ctx.Done()
		s.logger.Info("context cancelled, stopping vrf server")
		s.grpcSrv.GracefulStop()
		_ = ln.Close()
		return nil
	})

	eg.Go(func() error {
		addr := ln.Addr().String()
		s.logger.Info("starting vrf gRPC server", zap.String("addr", addr))
		if err := s.grpcSrv.Serve(ln); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("vrf gRPC server: %w", err)
		}
		return nil
	})

	return eg.Wait()
}

func (s *Server) Start(ctx context.Context, addr string) error {
	if strings.HasPrefix(addr, "unix://") {
		path := strings.TrimPrefix(addr, "unix://")
		if strings.TrimSpace(path) == "" {
			return errVrfUnixListenerPathEmpty
		}
		_ = os.Remove(path)

		ln, err := net.Listen("unix", path)
		if err != nil {
			return err
		}
		defer func() {
			_ = os.Remove(path)
		}()

		return s.StartWithListener(ctx, withPerClientUDSPeerIdentity(ln, s.logger))
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.StartWithListener(ctx, ln)
}

func (s *Server) Randomness(
	ctx context.Context,
	req *types.QueryRandomnessRequest,
) (*types.QueryRandomnessResponse, error) {
	if req == nil {
		return nil, errNilQueryRandomnessRequest
	}

	if err := s.allow(ctx, "Randomness"); err != nil {
		return nil, err
	}

	release := s.acquire("Randomness")
	defer release()

	res, err := s.svc.Randomness(ctx, req.Round)
	if err != nil {
		return nil, err
	}

	return res, nil
}

func (s *Server) Info(
	ctx context.Context,
	_ *types.QueryInfoRequest,
) (*types.QueryInfoResponse, error) {
	if err := s.allow(ctx, "Info"); err != nil {
		return nil, err
	}

	release := s.acquire("Info")
	defer release()

	info, err := s.svc.Info(ctx)
	if err != nil {
		return nil, err
	}

	return info, nil
}

func (s *Server) acquire(method string) func() {
	start := time.Now()
	s.sem <- struct{}{}
	s.metrics.ObserveGRPCConcurrencyWait(method, time.Since(start).Seconds())
	return func() {
		<-s.sem
	}
}

func (s *Server) allow(ctx context.Context, method string) error {
	// Do per-client limiting before global limiting. This prevents "burning" a
	// global token and then rejecting due to the per-client limiter.
	if !s.allowPerClient(ctx) {
		s.metrics.AddGRPCRateLimitRejected(method)
		return status.Error(codes.ResourceExhausted, "vrf: rate limit exceeded")
	}

	if s.limiter != nil && !s.limiter.Allow() {
		s.metrics.AddGRPCRateLimitRejected(method)
		return status.Error(codes.ResourceExhausted, "vrf: rate limit exceeded")
	}

	return nil
}

func (s *Server) allowPerClient(ctx context.Context) bool {
	if s.perClientRate == rate.Inf {
		return true
	}

	if s.perClientBurst <= 0 {
		// Misconfiguration; avoid panics and fall back to global-only limiting.
		return true
	}

	key := clientKeyFromContext(ctx)
	if strings.TrimSpace(key) == "" {
		key = "unknown"
	}

	lim := s.getOrCreatePerClientLimiter(key)
	if lim == nil {
		return true
	}
	return lim.Allow()
}

func (s *Server) getOrCreatePerClientLimiter(key string) *rate.Limiter {
	s.perClientMu.Lock()
	defer s.perClientMu.Unlock()

	if s.perClientLimiters == nil {
		return nil
	}

	if lim, ok := s.perClientLimiters.Get(key); ok {
		return lim
	}

	lim := rate.NewLimiter(s.perClientRate, s.perClientBurst)
	s.perClientLimiters.Add(key, lim)
	return lim
}

func clientKeyFromContext(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil || p.Addr == nil {
		return ""
	}

	// Policy decision:
	// - TCP: identify clients by peer IP (port ignored). Loopback clients that share an IP (e.g. 127.0.0.1)
	//   share limits; NAT'd clients also share limits.
	// - UDS: identify clients by a stable token (best-effort via peer credentials).
	switch addr := p.Addr.(type) {
	case interface{ ClientKey() string }:
		return addr.ClientKey()
	case *net.TCPAddr:
		if addr.IP == nil {
			return ""
		}
		return "tcp:" + addr.IP.String()
	case *net.UnixAddr:
		if strings.TrimSpace(addr.Name) == "" {
			return "uds:unknown"
		}
		return "uds:" + addr.Name
	default:
		str := strings.TrimSpace(p.Addr.String())
		if str == "" {
			return ""
		}
		return p.Addr.Network() + ":" + str
	}
}
