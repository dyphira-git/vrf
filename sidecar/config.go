package sidecar

// Config contains the static configuration for a single canonical drand chain
// as seen by a sidecar instance. All fields in this struct are expected to be
// consistent with the on-chain VrfParams for the network.
type Config struct {
	// DrandSupervise indicates whether the sidecar should start and supervise a
	// local drand subprocess. When false, drand is assumed to be managed
	// externally.
	DrandSupervise bool

	// DrandHTTP is the base URL of the local drand HTTP endpoint, e.g.
	// "http://127.0.0.1:8081". Only this statically configured endpoint is
	// ever used for randomness / info fetches.
	DrandHTTP string

	// BinaryPath is the path to the drand CLI binary. If empty, "drand" is
	// used.
	BinaryPath string

	// DrandVersionCheck controls enforcement of the compiled-in drand version pin
	// (PRD §3.1). In production, this should remain in strict mode.
	DrandVersionCheck DrandVersionCheckMode

	// DrandDataDir is the drand daemon folder (used when DrandSupervise is true).
	DrandDataDir string

	// DrandPrivateListen is the drand private listen address (used when DrandSupervise is true).
	DrandPrivateListen string

	// DrandPublicListen is the drand public listen address (used when DrandSupervise is true).
	DrandPublicListen string

	// DrandControlListen is the drand control listen address (used when DrandSupervise is true).
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
