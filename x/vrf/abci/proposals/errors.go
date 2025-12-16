package proposals

import "fmt"

// InvalidExtendedCommitInfoError is returned when the injected extended commit
// info is missing or fails validation.
type InvalidExtendedCommitInfoError struct {
	Err error
}

func (e InvalidExtendedCommitInfoError) Error() string {
	return fmt.Sprintf("invalid extended commit info: %v", e.Err)
}

func (InvalidExtendedCommitInfoError) Label() string {
	return "InvalidExtendedCommitInfoError"
}
