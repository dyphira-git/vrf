package types

import (
	"errors"
	"fmt"
)

var (
	errPeriodSecondsMustBePositive = errors.New("period_seconds must be positive")
	errSafetyMarginTooLow          = errors.New("safety_margin_seconds must be >= period_seconds")
)

// DefaultParams mirrors the PRD definition and contains all cryptographic and timing
// context needed to verify drand beacons on-chain and map block time to drand
// rounds.
func DefaultParams() VrfParams {
	return VrfParams{
		PeriodSeconds:       30,
		SafetyMarginSeconds: 30,
		Enabled:             false,
	}
}

func (p VrfParams) Validate() error {
	if p.PeriodSeconds == 0 {
		return errPeriodSecondsMustBePositive
	}

	if p.SafetyMarginSeconds < p.PeriodSeconds {
		return fmt.Errorf("%w: got %d, expected >=%d", errSafetyMarginTooLow, p.SafetyMarginSeconds, p.PeriodSeconds)
	}

	return nil
}
