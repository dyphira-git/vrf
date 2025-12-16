package vrf

import "errors"

var (
	errVrfServiceNil                     = errors.New("vrf service is nil")
	errVrfUnixListenerPathEmpty          = errors.New("vrf unix listener path cannot be empty")
	errVrfDebugHTTPUnixListenerPathEmpty = errors.New("vrf debug http unix listener path cannot be empty")
	errNilQueryRandomnessRequest         = errors.New("nil QueryRandomnessRequest")

	errGRPCMaxConnectionIdleNegative     = errors.New("grpc max connection idle must be >= 0")
	errGRPCMaxConnectionAgeNegative      = errors.New("grpc max connection age must be >= 0")
	errGRPCMaxConnectionAgeGraceNegative = errors.New("grpc max connection age grace must be >= 0")
	errGRPCKeepaliveTimeNegative         = errors.New("grpc keepalive time must be >= 0")
	errGRPCKeepaliveTimeoutNegative      = errors.New("grpc keepalive timeout must be >= 0")
	errGRPCKeepaliveMinTimeNegative      = errors.New("grpc keepalive min time must be >= 0")
	errGRPCMaxRecvMsgSizeNegative        = errors.New("grpc max recv msg size must be >= 0")
	errGRPCMaxSendMsgSizeNegative        = errors.New("grpc max send msg size must be >= 0")

	errConnNoRawFD                      = errors.New("connection does not expose a raw FD")
	errNilConn                          = errors.New("nil conn")
	errUnableToDeterminePeerCredentials = errors.New("unable to determine peer credentials")
)
