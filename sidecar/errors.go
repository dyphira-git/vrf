package sidecar

import "errors"

var (
	ErrRoundNotAvailable  = errors.New("sidecar: round not available")
	ErrWrongRound         = errors.New("sidecar: wrong round")
	ErrHashMismatch       = errors.New("sidecar: hash mismatch")
	ErrBadSignature       = errors.New("sidecar: bad signature")
	ErrServiceUnavailable = errors.New("sidecar: service unavailable")
)
