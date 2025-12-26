package drand

import "strings"

// Build-time pins for the drand binary semantics that this sidecar release
// supports. These are intended to be set via -ldflags at build time.
//
// Example:
//
//	-X github.com/vexxvakan/vrf/sidecar.expectedDrandSemver=2.2.0
//	-X github.com/vexxvakan/vrf/sidecar.expectedDrandCommit=deadbeef
var (
	expectedDrandSemver string
	expectedDrandCommit string
)

type expectedDrandVersion struct {
	semver string
	commit string
}

func getExpectedDrandVersion() expectedDrandVersion {
	return expectedDrandVersion{
		semver: strings.TrimSpace(expectedDrandSemver),
		commit: strings.TrimSpace(expectedDrandCommit),
	}
}
