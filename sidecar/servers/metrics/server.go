package metrics

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

const (
	maxOpenConnections = 3
	readHeaderTimeout  = 10 * time.Second
)

var (
	errInvalidPrometheusServerAddress = errors.New("invalid prometheus server address")
	errUnknownPrometheusListenNetwork = errors.New("unknown prometheus listen network")
)

// PrometheusServer is a prometheus server that serves metrics registered in the DefaultRegisterer.
// It is a wrapper around the promhttp.Handler() handler. The server will be started in a go-routine,
// and is gracefully stopped on close.
type PrometheusServer struct { //nolint
	srv    *http.Server
	done   chan struct{}
	logger *zap.Logger

	network string
	addr    string
}

// NewPrometheusServer creates a prometheus server if the metrics are enabled and
// address is set, and valid. Notice, this method does not start the server.
func NewPrometheusServer(prometheusAddress string, logger *zap.Logger) (*PrometheusServer, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	// get the prometheus server address
	network, addr, ok := parseListenAddress(prometheusAddress)
	if !ok {
		return nil, fmt.Errorf("%w: %s", errInvalidPrometheusServerAddress, prometheusAddress)
	}
	srv := &http.Server{
		Addr: addr,
		Handler: promhttp.InstrumentMetricHandler(
			prometheus.DefaultRegisterer, promhttp.HandlerFor(
				prometheus.DefaultGatherer,
				promhttp.HandlerOpts{MaxRequestsInFlight: maxOpenConnections},
			),
		),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	logger = logger.With(zap.String("server", "prometheus"))
	ps := &PrometheusServer{
		srv:     srv,
		done:    make(chan struct{}),
		logger:  logger,
		network: network,
		addr:    addr,
	}

	return ps, nil
}

// Start will spawn a http server that will handle requests to /metrics
// and serves the metrics registered in the DefaultRegisterer.
func (ps *PrometheusServer) Start() {
	var listener net.Listener
	var err error
	switch ps.network {
	case "unix":
		_ = os.Remove(ps.addr)
		listener, err = net.Listen("unix", ps.addr)
		if err == nil {
			defer func() { _ = os.Remove(ps.addr) }()
		}
	case "tcp":
		listener, err = net.Listen("tcp", ps.addr)
	default:
		err = fmt.Errorf("%w: %q", errUnknownPrometheusListenNetwork, ps.network)
	}

	if err != nil {
		ps.logger.Info("prometheus server error", zap.Error(err))
		close(ps.done)
		return
	}

	if err := ps.srv.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		ps.logger.Info("prometheus server error", zap.Error(err))
	} else {
		ps.logger.Info("prometheus server closed")
	}

	// close the done channel
	close(ps.done)
}

// Close gracefully closes the server.
func (ps *PrometheusServer) Close() {
	if ps == nil || ps.srv == nil {
		return
	}

	if err := ps.srv.Close(); err != nil {
		ps.logger.Info("prometheus server close error", zap.Error(err))
	}
}

// Done exposes a channel that is closed when the server stops.
func (ps *PrometheusServer) Done() <-chan struct{} {
	if ps == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return ps.done
}

func parseListenAddress(addr string) (network string, address string, ok bool) {
	if strings.HasPrefix(addr, "unix://") {
		path := strings.TrimPrefix(addr, "unix://")
		if strings.TrimSpace(path) == "" {
			return "", "", false
		}
		return "unix", path, true
	}

	if !isValidTCPAddress(addr) {
		return "", "", false
	}
	return "tcp", addr, true
}

func isValidTCPAddress(addr string) bool {
	if addr == "" {
		return false
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return false
	}

	if host == "" {
		return false
	}

	return true
}
