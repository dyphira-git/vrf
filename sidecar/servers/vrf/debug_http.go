package vrf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"go.uber.org/zap"
	"golang.org/x/net/netutil"
	"golang.org/x/sync/errgroup"

	"github.com/vexxvakan/vrf/sidecar/servers/vrf/types"
)

const (
	debugHTTPMaxConnections    = 16
	debugHTTPShutdownTimeout   = 5 * time.Second
	debugHTTPReadHeaderTimeout = 5 * time.Second
	debugHTTPReadTimeout       = 15 * time.Second
	debugHTTPWriteTimeout      = 15 * time.Second
	debugHTTPIdleTimeout       = 60 * time.Second
	debugHTTPMaxHeaderBytes    = 1 << 20
)

func (s *Server) StartDebugHTTP(ctx context.Context, addr string) error {
	if s.svc == nil {
		return errVrfServiceNil
	}

	if strings.HasPrefix(addr, "unix://") {
		path := strings.TrimPrefix(addr, "unix://")
		if strings.TrimSpace(path) == "" {
			return errVrfDebugHTTPUnixListenerPathEmpty
		}
		_ = os.Remove(path)

		ln, err := net.Listen("unix", path)
		if err != nil {
			return err
		}
		defer func() {
			_ = os.Remove(path)
		}()

		return s.startDebugHTTPWithListener(ctx, ln)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.startDebugHTTPWithListener(ctx, ln)
}

func (s *Server) startDebugHTTPWithListener(ctx context.Context, ln net.Listener) error {
	mux := runtime.NewServeMux()
	if err := types.RegisterVrfHandlerServer(ctx, mux, s); err != nil {
		_ = ln.Close()
		return fmt.Errorf("register vrf debug http handlers: %w", err)
	}

	httpSrv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: debugHTTPReadHeaderTimeout,
		ReadTimeout:       debugHTTPReadTimeout,
		WriteTimeout:      debugHTTPWriteTimeout,
		IdleTimeout:       debugHTTPIdleTimeout,
		MaxHeaderBytes:    debugHTTPMaxHeaderBytes,
	}

	logger := s.logger.With(zap.String("server", "vrf_debug_http"))

	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), debugHTTPShutdownTimeout)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		_ = ln.Close()
		return nil
	})

	eg.Go(func() error {
		addr := ln.Addr().String()
		logger.Info("starting vrf debug HTTP server", zap.String("addr", addr))
		limited := netutil.LimitListener(ln, debugHTTPMaxConnections)
		if err := httpSrv.Serve(limited); err != nil && !errors.Is(err, http.ErrServerClosed) {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("vrf debug http server: %w", err)
		}
		return nil
	})

	return eg.Wait()
}
