package sidecar

import "errors"

var (
	ErrRoundNotAvailable                 = errors.New("sidecar: round not available")
	ErrWrongRound                        = errors.New("sidecar: wrong round")
	ErrHashMismatch                      = errors.New("sidecar: hash mismatch")
	ErrBadSignature                      = errors.New("sidecar: bad signature")
	ErrServiceUnavailable                = errors.New("sidecar: service unavailable")
	ErrVrfServiceNil                     = errors.New("vrf service is nil")
	ErrVrfUnixListenerPathEmpty          = errors.New("vrf unix listener path cannot be empty")
	ErrVrfDebugHTTPUnixListenerPathEmpty = errors.New("vrf debug http unix listener path cannot be empty")
	ErrNilQueryRandomnessRequest         = errors.New("nil QueryRandomnessRequest")

	ErrGRPCMaxConnectionIdleNegative     = errors.New("grpc max connection idle must be >= 0")
	ErrGRPCMaxConnectionAgeNegative      = errors.New("grpc max connection age must be >= 0")
	ErrGRPCMaxConnectionAgeGraceNegative = errors.New("grpc max connection age grace must be >= 0")
	ErrGRPCKeepaliveTimeNegative         = errors.New("grpc keepalive time must be >= 0")
	ErrGRPCKeepaliveTimeoutNegative      = errors.New("grpc keepalive timeout must be >= 0")
	ErrGRPCKeepaliveMinTimeNegative      = errors.New("grpc keepalive min time must be >= 0")
	ErrGRPCMaxRecvMsgSizeNegative        = errors.New("grpc max recv msg size must be >= 0")
	ErrGRPCMaxSendMsgSizeNegative        = errors.New("grpc max send msg size must be >= 0")

	ErrConnNoRawFD                      = errors.New("connection does not expose a raw FD")
	ErrNilConn                          = errors.New("nil conn")
	ErrUnableToDeterminePeerCredentials = errors.New("unable to determine peer credentials")
)
