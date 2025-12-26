package drand

import (
	"errors"
	"strings"
)

// Config contains the static configuration for a single canonical drand chain
// as seen by a sidecar instance. All fields in this struct are expected to be
// consistent with the on-chain VrfParams for the network.
type Config struct {
	// DrandHTTP is the base URL of the local drand HTTP endpoint, e.g.
	// "http://127.0.0.1:8081". Only this statically configured endpoint is
	// ever used for randomness / info fetches.
	DrandHTTP string

	// DrandAllowNonLoopbackHTTP permits non-loopback drand HTTP endpoints.
	// This is unsafe for production but useful for containerized dev setups
	// where drand runs in a separate service on an isolated Docker network.
	DrandAllowNonLoopbackHTTP bool

	// BinaryPath is the path to the drand CLI binary. If empty, "drand" is
	// used.
	BinaryPath string

	// DrandVersionCheck controls enforcement of the compiled-in drand version pin
	// (PRD §3.1). In production, this should remain in strict mode.
	DrandVersionCheck DrandVersionCheckMode

	// DrandDataDir is the drand daemon folder used by the supervised drand subprocess.
	DrandDataDir string

	// DrandID is the drand beacon ID (defaults to "default" in drand).
	DrandID string

	// DrandPrivateListen is the drand private listen address used by the supervised drand subprocess.
	DrandPrivateListen string

	// DrandPublicListen is the drand public listen address used by the supervised drand subprocess.
	DrandPublicListen string

	// DrandControlListen is the drand control listen address used by the supervised drand subprocess.
	DrandControlListen string

	// ChainHash is the canonical drand chain hash.
	ChainHash []byte

	// PublicKey is the collective BLS public key for the drand group.
	PublicKey []byte

	// PeriodSeconds is the drand beacon period in seconds.
	PeriodSeconds uint64

	// GenesisUnixSec is the drand genesis time (UNIX seconds) for round 1.
	GenesisUnixSec int64
}

// ValidateBasic validates the config for sidecar runtime wiring.
//
// NOTE: This does not require chain parameters (hash/public key/period/genesis),
// because those may be supplied by the chain watcher at runtime.
func (c Config) ValidateBasic() error {
	if _, err := ParseDrandVersionCheckMode(string(c.DrandVersionCheck)); err != nil {
		return err
	}

	httpEndpoint := strings.TrimSpace(c.DrandHTTP)
	if httpEndpoint == "" {
		publicListen := strings.TrimSpace(c.DrandPublicListen)
		if publicListen != "" {
			httpEndpoint = "http://" + publicListen
		}
	}
	if httpEndpoint == "" {
		return errDrandHTTPEndpointRequired
	}

	if err := validateDrandHTTPEndpoint(httpEndpoint, c.DrandAllowNonLoopbackHTTP); err != nil {
		return err
	}

	if strings.TrimSpace(c.DrandDataDir) == "" {
		return errDrandDataDirRequired
	}
	if strings.TrimSpace(c.DrandPrivateListen) == "" {
		return errDrandPrivateListenAddrRequired
	}
	if strings.TrimSpace(c.DrandPublicListen) == "" {
		return errDrandPublicListenAddrRequired
	}
	if strings.TrimSpace(c.DrandControlListen) == "" {
		return errDrandControlListenAddrRequired
	}

	return nil
}

func validateDrandHTTPEndpoint(endpoint string, allowNonLoopback bool) error {
	if !allowNonLoopback {
		return enforceLoopbackHTTP(endpoint)
	}

	if err := enforceLoopbackHTTP(endpoint); err != nil {
		if errors.Is(err, errDrandHTTPEndpointNotLoopback) {
			return nil
		}
		return err
	}

	return nil
}
