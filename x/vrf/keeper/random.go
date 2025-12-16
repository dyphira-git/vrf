package keeper

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/vexxvakan/vrf/x/vrf/types"
)

var (
	errGetBeaconWhileDisabled           = errors.New("vrf: GetBeacon called while VRF is disabled")
	errExpandRandomnessCountZero        = errors.New("vrf: ExpandRandomness requires count > 0")
	errDeriveRandomWordsCountZero       = errors.New("vrf: deriveRandomWords requires count > 0")
	errDeriveRandomWordsEmptyRandomness = errors.New("vrf: deriveRandomWords requires non-empty beacon randomness")
)

// GetBeacon returns the VrfBeacon for the current context height
//
//   - If VrfParams.enabled == false, returns an error.
//   - If there is no VrfBeacon for the current height, returns an error.
func (k Keeper) GetBeacon(ctx sdk.Context) (types.VrfBeacon, error) {
	storeCtx := sdk.WrapSDKContext(ctx)

	params, err := k.GetParams(storeCtx)
	if err != nil {
		return types.VrfBeacon{}, err
	}

	if !params.Enabled {
		return types.VrfBeacon{}, errGetBeaconWhileDisabled
	}

	beacon, err := k.GetLatestBeacon(storeCtx)
	if err != nil {
		return types.VrfBeacon{}, err
	}

	return beacon, nil
}

// ExpandRandomness derives `count` 32-byte "random words" from the beacon in
// the current context using a simple deterministic expansion:
//
//	sha256(seed || userSeed || uint32(i))
//
// It returns an error if VRF is disabled or if no beacon exists for the
// current height.
func (k Keeper) ExpandRandomness(
	ctx sdk.Context,
	count uint32,
	userSeed []byte,
) (types.VrfBeacon, [][]byte, error) {
	if count == 0 {
		return types.VrfBeacon{}, nil, errExpandRandomnessCountZero
	}

	beacon, err := k.GetBeacon(ctx)
	if err != nil {
		return types.VrfBeacon{}, nil, err
	}

	words, err := deriveRandomWords(beacon, count, userSeed)
	if err != nil {
		return types.VrfBeacon{}, nil, err
	}

	return beacon, words, nil
}

// deriveRandomWords expands the beacon seed into `count` 32-byte words using:
//
//	sha256(seed || userSeed || uint32(i))
func deriveRandomWords(
	beacon types.VrfBeacon,
	count uint32,
	userSeed []byte,
) ([][]byte, error) {
	if count == 0 {
		return nil, errDeriveRandomWordsCountZero
	}

	if len(beacon.Randomness) == 0 {
		return nil, errDeriveRandomWordsEmptyRandomness
	}

	out := make([][]byte, 0, count)

	hasher := sha256.New()
	var ctr [4]byte

	for i := range count {
		hasher.Reset()
		_, _ = hasher.Write(beacon.Randomness)
		_, _ = hasher.Write(userSeed)
		binary.BigEndian.PutUint32(ctr[:], i)
		_, _ = hasher.Write(ctr[:])
		word := make([]byte, 0, sha256.Size)
		word = hasher.Sum(word)
		out = append(out, word)
	}

	return out, nil
}
