package vrf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
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
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	sidecarv1 "github.com/vexxvakan/vrf/api/vexxvakan/sidecar/v1"
	"github.com/vexxvakan/vrf/sidecar"
	scerror "github.com/vexxvakan/vrf/sidecar/errors"
	sidecarmetrics "github.com/vexxvakan/vrf/sidecar/servers/metrics"
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
	sidecarv1.UnimplementedVrfServer

	svc     sidecar.Service
	grpcSrv *grpc.Server
	logger  *zap.Logger
	metrics sidecarmetrics.Metrics

	grpcOpts []grpc.ServerOption
	newGRPC  func(opts ...grpc.ServerOption) *grpc.Server

	extraRegistrations []func(grpc.ServiceRegistrar)
	debugRegistrations []func(*http.ServeMux)

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

func (s *Server) RegisterServices(fn func(grpc.ServiceRegistrar)) {
	if s == nil || fn == nil {
		return
	}
	s.extraRegistrations = append(s.extraRegistrations, fn)
}

func (s *Server) RegisterDebugRoutes(fn func(*http.ServeMux)) {
	if s == nil || fn == nil {
		return
	}
	s.debugRegistrations = append(s.debugRegistrations, fn)
}

func (s *Server) StartWithListener(ctx context.Context, ln net.Listener) error {
	if s.svc == nil {
		return scerror.ErrVrfServiceNil
	}

	newGRPC := s.newGRPC
	if newGRPC == nil {
		newGRPC = grpc.NewServer
	}

	s.grpcSrv = newGRPC(s.grpcOpts...)
	sidecarv1.RegisterVrfServer(s.grpcSrv, s)
	for _, fn := range s.extraRegistrations {
		if fn == nil {
			continue
		}
		fn(s.grpcSrv)
	}

	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		<-ctx.Done()
		s.logger.Info("context cancelled, stopping vrf gRPC server")
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
			return scerror.ErrVrfUnixListenerPathEmpty
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
	req *sidecarv1.QueryRandomnessRequest,
) (*sidecarv1.QueryRandomnessResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil QueryRandomnessRequest")
	}

	if err := s.allow(ctx, "Randomness"); err != nil {
		return nil, err
	}

	release := s.acquire("Randomness")
	defer release()

	res, err := s.svc.Randomness(ctx, req.Round)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return res, nil
}

func (s *Server) Info(
	ctx context.Context,
	_ *sidecarv1.QueryInfoRequest,
) (*sidecarv1.QueryInfoResponse, error) {
	if err := s.allow(ctx, "Info"); err != nil {
		return nil, err
	}

	release := s.acquire("Info")
	defer release()

	info, err := s.svc.Info(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return info, nil
}

func mapServiceError(err error) error {
	switch {
	case errors.Is(err, scerror.ErrServiceUnavailable):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, scerror.ErrRoundNotAvailable):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, scerror.ErrWrongRound):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, scerror.ErrBadSignature), errors.Is(err, scerror.ErrHashMismatch):
		return status.Error(codes.DataLoss, err.Error())
	default:
		return err
	}
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

func (s *Server) StartDebugHTTP(ctx context.Context, addr string) error {
	if s.svc == nil {
		return scerror.ErrVrfServiceNil
	}

	if strings.TrimSpace(addr) == "" {
		return errors.New("debug http addr is empty")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/vrf/v1/info", s.handleDebugInfo)
	mux.HandleFunc("/vrf/v1/randomness", s.handleDebugRandomness)
	for _, fn := range s.debugRegistrations {
		if fn == nil {
			continue
		}
		fn(mux)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	var (
		ln      net.Listener
		cleanup func()
	)

	if strings.HasPrefix(addr, "unix://") {
		path := strings.TrimPrefix(addr, "unix://")
		if strings.TrimSpace(path) == "" {
			return scerror.ErrVrfDebugHTTPUnixListenerPathEmpty
		}
		_ = os.Remove(path)
		unixLn, err := net.Listen("unix", path)
		if err != nil {
			return err
		}
		ln = unixLn
		cleanup = func() { _ = os.Remove(path) }
	} else {
		tcpLn, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		ln = tcpLn
	}

	if cleanup == nil {
		cleanup = func() {}
	}
	defer cleanup()

	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		_ = ln.Close()
		return nil
	})

	eg.Go(func() error {
		s.logger.Info("starting vrf debug HTTP server", zap.String("addr", ln.Addr().String()))
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("vrf debug http server: %w", err)
		}
		return nil
	})

	return eg.Wait()
}

func (s *Server) handleDebugInfo(w http.ResponseWriter, r *http.Request) {
	info, err := s.svc.Info(r.Context())
	if err != nil {
		writeDebugError(w, err)
		return
	}

	writeProtoJSON(w, info)
}

func (s *Server) handleDebugRandomness(w http.ResponseWriter, r *http.Request) {
	round := uint64(0)
	if q := strings.TrimSpace(r.URL.Query().Get("round")); q != "" {
		parsed, err := strconv.ParseUint(q, 10, 64)
		if err != nil {
			http.Error(w, "invalid round", http.StatusBadRequest)
			return
		}
		round = parsed
	}

	res, err := s.svc.Randomness(r.Context(), round)
	if err != nil {
		writeDebugError(w, err)
		return
	}

	writeProtoJSON(w, res)
}

func writeProtoJSON(w http.ResponseWriter, msg proto.Message) {
	b, err := protojson.MarshalOptions{
		UseProtoNames: true,
	}.Marshal(msg)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func writeDebugError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, scerror.ErrServiceUnavailable):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, scerror.ErrRoundNotAvailable):
		http.Error(w, err.Error(), http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
