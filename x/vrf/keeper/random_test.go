package keeper

import (
	"encoding/hex"

	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

func (s *KeeperSuite) TestDeriveRandomWords() {
	beacon := vrftypes.VrfBeacon{
		DrandRound: 0x1122334455667788,
		// Exactly 32 bytes
		Randomness: []byte("abcdefghijklmnopqrstuvwxyz012345"),
	}

	words, err := deriveRandomWords(beacon, 3, []byte{0xaa, 0xbb, 0xcc})
	s.Require().NoError(err)
	s.Require().Len(words, 3)
	for _, w := range words {
		s.Require().Len(w, 32)
	}

	s.Require().Equal("a53168fa4e0b142bc59542cdaffdf937fb75732d59d6cfdfd970decdbe5faeea", hex.EncodeToString(words[0]))
	s.Require().Equal("d4d1854ac055b522d9dfcef743b366b4e1e5ffbcec29552c5df9ab40d3b501aa", hex.EncodeToString(words[1]))
	s.Require().Equal("52c96fcae7cff56944f1ad1f0aec4afc39b0fda70900c34c4fb19b54f8d51b53", hex.EncodeToString(words[2]))
}

func (s *KeeperSuite) TestDeriveRandomWordsCountZero() {
	_, err := deriveRandomWords(vrftypes.VrfBeacon{}, 0, nil)
	s.Require().Error(err)
}
