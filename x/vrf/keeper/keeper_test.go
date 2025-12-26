package keeper

import (
	"errors"

	"cosmossdk.io/collections"

	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"

	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

func (s *KeeperSuite) TestParams() {
	params, err := s.Keeper.GetParams(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(vrftypes.DefaultParams(), params)

	bad := vrftypes.VrfParams{PeriodSeconds: 0, SafetyMarginSeconds: 0}
	s.Require().Error(s.Keeper.SetParams(s.Ctx, bad))
}

func (s *KeeperSuite) TestParamsUpdatedHeight() {
	h, err := s.Keeper.GetParamsUpdatedHeight(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(int64(0), h)

	s.Require().NoError(s.Keeper.SetParamsUpdatedHeight(s.Ctx, 42))
	h, err = s.Keeper.GetParamsUpdatedHeight(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(int64(42), h)
}

func (s *KeeperSuite) TestBlockTimes() {
	prev, err := s.Keeper.GetPrevBlockTime(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(int64(0), prev)

	s.Require().NoError(s.Keeper.SetLastBlockTime(s.Ctx, 100))
	last, err := s.Keeper.GetLastBlockTime(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(int64(100), last)
	prev, err = s.Keeper.GetPrevBlockTime(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(int64(100), prev)

	s.Require().NoError(s.Keeper.SetLastBlockTime(s.Ctx, 200))
	last, err = s.Keeper.GetLastBlockTime(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(int64(200), last)
	prev, err = s.Keeper.GetPrevBlockTime(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(int64(100), prev)
}

func (s *KeeperSuite) TestCommittee() {
	authority := s.Keeper.GetAuthority()
	ok, err := s.Keeper.IsCommitteeMember(s.Ctx, authority)
	s.Require().NoError(err)
	s.Require().True(ok)

	s.Require().ErrorIs(s.Keeper.RemoveCommitteeMember(s.Ctx, authority), errCannotRemoveModuleAuthority)

	_, _, addr := testdata.KeyTestPubAddr()
	addrStr := addr.String()
	s.Require().NoError(s.Keeper.SetCommitteeMember(s.Ctx, addrStr, "test"))
	ok, err = s.Keeper.IsCommitteeMember(s.Ctx, addrStr)
	s.Require().NoError(err)
	s.Require().True(ok)

	label, err := s.Keeper.committee.Get(s.Ctx, addrStr)
	s.Require().NoError(err)
	s.Require().Equal("test", label)

	s.Require().NoError(s.Keeper.RemoveCommitteeMember(s.Ctx, addrStr))
	ok, err = s.Keeper.IsCommitteeMember(s.Ctx, addrStr)
	s.Require().NoError(err)
	s.Require().False(ok)
}

func (s *KeeperSuite) TestIdentity() {
	_, _, addr := testdata.KeyTestPubAddr()
	valAddr := sdk.ValAddress(addr).String()
	identity := vrftypes.VrfIdentity{
		ValidatorAddress:  valAddr,
		DrandBlsPublicKey: []byte("pk"),
	}

	s.Require().NoError(s.Keeper.SetVrfIdentity(s.Ctx, identity))
	got, err := s.Keeper.identities.Get(s.Ctx, valAddr)
	s.Require().NoError(err)
	s.Require().Equal(identity, got)

	s.Require().NoError(s.Keeper.RemoveVrfIdentity(s.Ctx, valAddr))
	_, err = s.Keeper.identities.Get(s.Ctx, valAddr)
	s.Require().True(errors.Is(err, collections.ErrNotFound))
}

func (s *KeeperSuite) TestGetBeacon() {
	_, err := s.Keeper.GetBeacon(s.Ctx)
	s.Require().ErrorIs(err, errGetBeaconWhileDisabled)

	params := vrftypes.DefaultParams()
	params.Enabled = true
	s.Require().NoError(s.Keeper.SetParams(s.Ctx, params))
	_, err = s.Keeper.GetBeacon(s.Ctx)
	s.Require().Error(err)

	beacon := vrftypes.VrfBeacon{DrandRound: 7, Randomness: []byte("rand")}
	s.Require().NoError(s.Keeper.SetLatestBeacon(s.Ctx, beacon))
	got, err := s.Keeper.GetBeacon(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(beacon, got)
}

func (s *KeeperSuite) TestExpandRandomness() {
	_, _, err := s.Keeper.ExpandRandomness(s.Ctx, 0, nil)
	s.Require().ErrorIs(err, errExpandRandomnessCountZero)

	_, _, err = s.Keeper.ExpandRandomness(s.Ctx, 1, nil)
	s.Require().ErrorIs(err, errGetBeaconWhileDisabled)

	params := vrftypes.DefaultParams()
	params.Enabled = true
	s.Require().NoError(s.Keeper.SetParams(s.Ctx, params))
	beacon := vrftypes.VrfBeacon{DrandRound: 3, Randomness: []byte("abcdefghijklmnopqrstuvwxyz012345")}
	s.Require().NoError(s.Keeper.SetLatestBeacon(s.Ctx, beacon))

	gotBeacon, words, err := s.Keeper.ExpandRandomness(s.Ctx, 2, []byte{0x01})
	s.Require().NoError(err)
	s.Require().Equal(beacon, gotBeacon)
	s.Require().Len(words, 2)
	for _, word := range words {
		s.Require().Len(word, 32)
	}
}
