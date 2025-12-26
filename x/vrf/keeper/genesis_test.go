package keeper

import (
	"time"

	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"

	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

func (s *KeeperSuite) TestInitGenesis() {
	s.Ctx = s.Ctx.WithBlockHeight(5).WithBlockTime(time.Unix(1700001234, 0).UTC())

	_, _, addr := testdata.KeyTestPubAddr()
	member := addr.String()
	valAddr := sdk.ValAddress(addr).String()
	beacon := vrftypes.VrfBeacon{DrandRound: 9, Randomness: []byte("beacon")}
	identity := vrftypes.VrfIdentity{
		ValidatorAddress:  valAddr,
		DrandBlsPublicKey: []byte("pk"),
	}

	gs := vrftypes.GenesisState{
		Params:       vrftypes.DefaultParams(),
		LatestBeacon: &beacon,
		Committee:    []vrftypes.AllowlistEntry{{Address: member, Label: "member"}},
		Identities:   []vrftypes.VrfIdentity{identity},
	}

	s.Keeper.InitGenesis(s.Ctx, gs)

	params, err := s.Keeper.GetParams(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(gs.Params, params)

	gotBeacon, err := s.Keeper.GetLatestBeacon(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(beacon, gotBeacon)

	ok, err := s.Keeper.IsCommitteeMember(s.Ctx, member)
	s.Require().NoError(err)
	s.Require().True(ok)

	ok, err = s.Keeper.IsCommitteeMember(s.Ctx, s.Keeper.GetAuthority())
	s.Require().NoError(err)
	s.Require().True(ok)

	stored, err := s.Keeper.identities.Get(s.Ctx, valAddr)
	s.Require().NoError(err)
	s.Require().Equal(identity, stored)

	last, err := s.Keeper.GetLastBlockTime(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(s.Ctx.BlockTime().Unix(), last)

	prev, err := s.Keeper.GetPrevBlockTime(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(s.Ctx.BlockTime().Unix(), prev)

	h, err := s.Keeper.GetParamsUpdatedHeight(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(s.Ctx.BlockHeight(), h)
}

func (s *KeeperSuite) TestInitGenesisPanics() {
	gs := vrftypes.GenesisState{
		Params: vrftypes.VrfParams{PeriodSeconds: 0, SafetyMarginSeconds: 0},
	}

	s.Require().Panics(func() {
		s.Keeper.InitGenesis(s.Ctx, gs)
	})
}

func (s *KeeperSuite) TestExportGenesis() {
	gs := s.Keeper.ExportGenesis(s.Ctx)
	s.Require().NotNil(gs)
	s.Require().Equal(vrftypes.DefaultParams(), gs.Params)

	foundAuthority := false
	for _, entry := range gs.Committee {
		if entry.Address == s.Keeper.GetAuthority() {
			foundAuthority = true
		}
	}
	s.Require().True(foundAuthority)

	beacon := vrftypes.VrfBeacon{DrandRound: 11, Randomness: []byte("next")}
	s.Require().NoError(s.Keeper.SetLatestBeacon(s.Ctx, beacon))
	s.Require().NoError(s.Keeper.SetCommitteeMember(s.Ctx, s.Keeper.GetAuthority(), "authority"))

	exported := s.Keeper.ExportGenesis(s.Ctx)
	s.Require().NotNil(exported.LatestBeacon)
	s.Require().Equal(beacon, *exported.LatestBeacon)
}
