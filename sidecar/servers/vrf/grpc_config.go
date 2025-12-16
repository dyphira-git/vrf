package vrf

import (
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

const (
	defaultGRPCKeepaliveTime    = 2 * time.Hour
	defaultGRPCKeepaliveTimeout = 20 * time.Second
	defaultGRPCKeepaliveMinTime = 5 * time.Minute
)

// GRPCServerConfig configures production-oriented hardening knobs for the sidecar's gRPC server.
//
// Zero values generally mean "use gRPC defaults" (or infinity for max-connection ages).
// Options are only applied when their corresponding fields are explicitly set to non-zero (or true for booleans).
type GRPCServerConfig struct {
	// Keepalive/max-age parameters (grpc keepalive.ServerParameters).
	MaxConnectionIdle     time.Duration
	MaxConnectionAge      time.Duration
	MaxConnectionAgeGrace time.Duration
	KeepaliveTime         time.Duration
	KeepaliveTimeout      time.Duration

	// Keepalive enforcement policy (grpc keepalive.EnforcementPolicy).
	KeepaliveMinTime             time.Duration
	KeepalivePermitWithoutStream bool

	// Resource limiting knobs.
	MaxConcurrentStreams uint32
	MaxRecvMsgSize       int
	MaxSendMsgSize       int
}

func (c GRPCServerConfig) Validate() error {
	if c.MaxConnectionIdle < 0 {
		return errGRPCMaxConnectionIdleNegative
	}
	if c.MaxConnectionAge < 0 {
		return errGRPCMaxConnectionAgeNegative
	}
	if c.MaxConnectionAgeGrace < 0 {
		return errGRPCMaxConnectionAgeGraceNegative
	}
	if c.KeepaliveTime < 0 {
		return errGRPCKeepaliveTimeNegative
	}
	if c.KeepaliveTimeout < 0 {
		return errGRPCKeepaliveTimeoutNegative
	}
	if c.KeepaliveMinTime < 0 {
		return errGRPCKeepaliveMinTimeNegative
	}
	if c.MaxRecvMsgSize < 0 {
		return errGRPCMaxRecvMsgSizeNegative
	}
	if c.MaxSendMsgSize < 0 {
		return errGRPCMaxSendMsgSizeNegative
	}
	return nil
}

func (c GRPCServerConfig) keepaliveParams() (keepalive.ServerParameters, bool) {
	apply := c.MaxConnectionIdle != 0 ||
		c.MaxConnectionAge != 0 ||
		c.MaxConnectionAgeGrace != 0 ||
		c.KeepaliveTime != 0 ||
		c.KeepaliveTimeout != 0
	if !apply {
		return keepalive.ServerParameters{}, false
	}

	timeVal := c.KeepaliveTime
	if timeVal == 0 {
		timeVal = defaultGRPCKeepaliveTime
	}
	timeoutVal := c.KeepaliveTimeout
	if timeoutVal == 0 {
		timeoutVal = defaultGRPCKeepaliveTimeout
	}

	return keepalive.ServerParameters{
		MaxConnectionIdle:     c.MaxConnectionIdle,
		MaxConnectionAge:      c.MaxConnectionAge,
		MaxConnectionAgeGrace: c.MaxConnectionAgeGrace,
		Time:                  timeVal,
		Timeout:               timeoutVal,
	}, true
}

func (c GRPCServerConfig) enforcementPolicy() (keepalive.EnforcementPolicy, bool) {
	apply := c.KeepaliveMinTime != 0 || c.KeepalivePermitWithoutStream
	if !apply {
		return keepalive.EnforcementPolicy{}, false
	}

	minTime := c.KeepaliveMinTime
	if minTime == 0 {
		minTime = defaultGRPCKeepaliveMinTime
	}

	return keepalive.EnforcementPolicy{
		MinTime:             minTime,
		PermitWithoutStream: c.KeepalivePermitWithoutStream,
	}, true
}

func (c GRPCServerConfig) serverOptions() ([]grpc.ServerOption, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	var opts []grpc.ServerOption
	if kp, ok := c.keepaliveParams(); ok {
		opts = append(opts, grpc.KeepaliveParams(kp))
	}
	if ep, ok := c.enforcementPolicy(); ok {
		opts = append(opts, grpc.KeepaliveEnforcementPolicy(ep))
	}

	if c.MaxConcurrentStreams > 0 {
		opts = append(opts, grpc.MaxConcurrentStreams(c.MaxConcurrentStreams))
	}
	if c.MaxRecvMsgSize > 0 {
		opts = append(opts, grpc.MaxRecvMsgSize(c.MaxRecvMsgSize))
	}
	if c.MaxSendMsgSize > 0 {
		opts = append(opts, grpc.MaxSendMsgSize(c.MaxSendMsgSize))
	}

	return opts, nil
}
