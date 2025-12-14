package vrf

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/vexxvakan/vrf/sidecar"
	sidecarmetrics "github.com/vexxvakan/vrf/sidecar/servers/prometheus/metrics"
	"github.com/vexxvakan/vrf/sidecar/servers/vrf/types"
)

const (
	defaultMaxConcurrent = 64
	defaultRatePerSecond = 100
	defaultRateBurst     = 200
)

type Server struct {
	types.VrfServer

	svc sidecar.Service

	grpcSrv *grpc.Server
	logger  *zap.Logger
	metrics sidecarmetrics.Metrics

	sem     chan struct{}
	limiter *rate.Limiter
}

func NewServer(svc sidecar.Service, logger *zap.Logger, m sidecarmetrics.Metrics) *Server {
	if logger == nil {
		logger = zap.NewNop()
	}

	if m == nil {
		m = sidecarmetrics.NewNop()
	}

	return &Server{
		svc:     svc,
		logger:  logger.With(zap.String("server", "vrf")),
		metrics: m,
		sem:     make(chan struct{}, defaultMaxConcurrent),
		limiter: rate.NewLimiter(
			defaultRatePerSecond,
			defaultRateBurst,
		),
	}
}

func (s *Server) StartWithListener(ctx context.Context, ln net.Listener) error {
	if s.svc == nil {
		return fmt.Errorf("vrf service is nil")
	}

	s.grpcSrv = grpc.NewServer()
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
			return fmt.Errorf("vrf unix listener path cannot be empty")
		}
		_ = os.Remove(path)

		ln, err := net.Listen("unix", path)
		if err != nil {
			return err
		}
		defer func() {
			_ = os.Remove(path)
		}()

		return s.StartWithListener(ctx, ln)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.StartWithListener(ctx, ln)
}

func (s *Server) acquire(method string) func() {
	start := time.Now()
	s.sem <- struct{}{}
	s.metrics.ObserveGRPCConcurrencyWait(method, time.Since(start).Seconds())
	return func() {
		<-s.sem
	}
}

func (s *Server) Randomness(
	ctx context.Context,
	req *types.QueryRandomnessRequest,
) (*types.QueryRandomnessResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("nil QueryRandomnessRequest")
	}

	if !s.limiter.Allow() {
		s.metrics.AddGRPCRateLimitRejected("Randomness")
		return nil, status.Error(codes.ResourceExhausted, "vrf: rate limit exceeded")
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
	if !s.limiter.Allow() {
		s.metrics.AddGRPCRateLimitRejected("Info")
		return nil, status.Error(codes.ResourceExhausted, "vrf: rate limit exceeded")
	}

	release := s.acquire("Info")
	defer release()

	info, err := s.svc.Info(ctx)
	if err != nil {
		return nil, err
	}

	return info, nil
}
