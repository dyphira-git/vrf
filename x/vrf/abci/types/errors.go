package types

import (
	"fmt"
)

// NilRequestError is an error that is returned when a nil request is given to the handler.
type NilRequestError struct {
	Handler ABCIMethod
}

func (e NilRequestError) Error() string {
	return fmt.Sprintf("nil request for %s", e.Handler)
}

func (NilRequestError) Label() string {
	return "NilRequestError"
}

// WrappedHandlerError is an error that is returned when a handler that is wrapped by a Connect ABCI handler
// returns an error.
type WrappedHandlerError struct {
	Handler ABCIMethod
	Err     error
}

func (e WrappedHandlerError) Error() string {
	return fmt.Sprintf("wrapped %s failed: %s", e.Handler, e.Err.Error())
}

func (WrappedHandlerError) Label() string {
	return "WrappedHandlerError"
}

// CodecError is an error that is returned when a codec fails to marshal or unmarshal a type.
type CodecError struct {
	Err error
}

func (e CodecError) Error() string {
	return fmt.Sprintf("codec error: %s", e.Err.Error())
}

func (CodecError) Label() string {
	return "CodecError"
}

// MissingCommitInfoError is an error that is returned when a proposal is missing the CommitInfo from the previous
// height.
type MissingCommitInfoError struct{}

func (MissingCommitInfoError) Error() string {
	return "missing commit info"
}

func (MissingCommitInfoError) Label() string {
	return "MissingCommitInfoError"
}
