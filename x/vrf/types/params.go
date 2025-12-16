package types

import (
	"errors"
	"fmt"
)

var (
	errPeriodSecondsMustBePositive  = errors.New("period_seconds must be positive")
	errSafetyMarginTooLow           = errors.New("safety_margin_seconds must be >= period_seconds")
	errPublicKeyRequiredWhenEnabled = errors.New("public_key must not be empty when enabled")
	errChainHashRequiredWhenEnabled = errors.New("chain_hash must not be empty when enabled")
)

// DefaultParams mirrors the PRD definition and contains all cryptographic and timing
// context needed to verify drand beacons on-chain and map block time to drand
// rounds.
func DefaultParams() VrfParams {
	return VrfParams{
		PeriodSeconds:       30,
		SafetyMarginSeconds: 30,
	}
}

func (p VrfParams) Validate() error {
	if p.PeriodSeconds == 0 {
		return errPeriodSecondsMustBePositive
	}

	if p.SafetyMarginSeconds < p.PeriodSeconds {
		return fmt.Errorf("%w: got %d, expected >=%d", errSafetyMarginTooLow, p.SafetyMarginSeconds, p.PeriodSeconds)
	}

	if p.Enabled {
		if len(p.PublicKey) == 0 {
			return errPublicKeyRequiredWhenEnabled
		}

		if len(p.ChainHash) == 0 {
			return errChainHashRequiredWhenEnabled
		}
	}

	return nil
}
