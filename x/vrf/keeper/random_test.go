package keeper

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vexxvakan/vrf/x/vrf/types"
)

func TestDeriveRandomWords_ReferenceVectors(t *testing.T) {
	t.Parallel()

	beacon := types.VrfBeacon{
		DrandRound: 0x1122334455667788,
		// Exactly 32 bytes.
		Randomness: []byte("abcdefghijklmnopqrstuvwxyz012345"),
	}

	words, err := deriveRandomWords(beacon, 3, []byte{0xaa, 0xbb, 0xcc})
	require.NoError(t, err)
	require.Len(t, words, 3)
	for _, w := range words {
		require.Len(t, w, 32)
	}

	require.Equal(t, "a53168fa4e0b142bc59542cdaffdf937fb75732d59d6cfdfd970decdbe5faeea", hex.EncodeToString(words[0]))
	require.Equal(t, "d4d1854ac055b522d9dfcef743b366b4e1e5ffbcec29552c5df9ab40d3b501aa", hex.EncodeToString(words[1]))
	require.Equal(t, "52c96fcae7cff56944f1ad1f0aec4afc39b0fda70900c34c4fb19b54f8d51b53", hex.EncodeToString(words[2]))
}

func TestDeriveRandomWords_CountZero(t *testing.T) {
	t.Parallel()

	_, err := deriveRandomWords(types.VrfBeacon{}, 0, nil)
	require.Error(t, err)
}
