package keeper

import (
	"time"

	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"

	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

func (s *KeeperSuite) TestMsgUpdateParams() {
	s.Ctx = s.Ctx.WithBlockHeight(12).WithBlockTime(time.Unix(1700002000, 0).UTC())
	_, _, addr := testdata.KeyTestPubAddr()
	msg := &vrftypes.MsgUpdateParams{
		Authority: addr.String(),
		Params:    vrftypes.DefaultParams(),
	}
	_, err := s.MsgServer.UpdateParams(s.Ctx, msg)
	s.Require().ErrorIs(err, errInvalidAuthority)

	params := vrftypes.DefaultParams()
	params.Enabled = true
	msg = &vrftypes.MsgUpdateParams{
		Authority: s.Keeper.GetAuthority(),
		Params:    params,
	}
	_, err = s.MsgServer.UpdateParams(s.Ctx, msg)
	s.Require().NoError(err)

	stored, err := s.Keeper.GetParams(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(params, stored)

	h, err := s.Keeper.GetParamsUpdatedHeight(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(s.Ctx.BlockHeight(), h)
}

func (s *KeeperSuite) TestMsgCommittee() {
	_, _, addr := testdata.KeyTestPubAddr()
	member := addr.String()

	msg := &vrftypes.MsgAddVrfCommitteeMember{
		Authority: member,
		Address:   member,
		Label:     "member",
	}
	_, err := s.MsgServer.AddVrfCommitteeMember(s.Ctx, msg)
	s.Require().ErrorIs(err, errInvalidAuthority)

	msg.Authority = s.Keeper.GetAuthority()
	_, err = s.MsgServer.AddVrfCommitteeMember(s.Ctx, msg)
	s.Require().NoError(err)

	label, err := s.Keeper.committee.Get(s.Ctx, member)
	s.Require().NoError(err)
	s.Require().Equal("member", label)

	remove := &vrftypes.MsgRemoveVrfCommitteeMember{
		Authority: member,
		Address:   member,
	}
	_, err = s.MsgServer.RemoveVrfCommitteeMember(s.Ctx, remove)
	s.Require().ErrorIs(err, errInvalidAuthority)

	remove.Authority = s.Keeper.GetAuthority()
	_, err = s.MsgServer.RemoveVrfCommitteeMember(s.Ctx, remove)
	s.Require().NoError(err)

	ok, err := s.Keeper.IsCommitteeMember(s.Ctx, member)
	s.Require().NoError(err)
	s.Require().False(ok)

	remove.Address = s.Keeper.GetAuthority()
	_, err = s.MsgServer.RemoveVrfCommitteeMember(s.Ctx, remove)
	s.Require().ErrorIs(err, errCannotRemoveModuleAuthority)
}

func (s *KeeperSuite) TestMsgInitialDkg() {
	_, _, addr := testdata.KeyTestPubAddr()
	initiator := addr.String()

	msg := &vrftypes.MsgInitialDkg{
		Initiator:      initiator,
		ChainHash:      []byte{0x01},
		PublicKey:      []byte("pk"),
		PeriodSeconds:  60,
		GenesisUnixSec: 1700002100,
	}
	_, err := s.MsgServer.InitialDkg(s.Ctx, msg)
	s.Require().ErrorIs(err, errInitiatorNotInCommittee)

	s.Require().NoError(s.Keeper.SetCommitteeMember(s.Ctx, initiator, "init"))
	_, err = s.MsgServer.InitialDkg(s.Ctx, msg)
	s.Require().NoError(err)

	params, err := s.Keeper.GetParams(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(msg.ChainHash, params.ChainHash)
	s.Require().Equal(msg.PublicKey, params.PublicKey)
	s.Require().Equal(msg.PeriodSeconds, params.PeriodSeconds)
	s.Require().Equal(msg.GenesisUnixSec, params.GenesisUnixSec)
	s.Require().Equal(uint64(1), params.ReshareEpoch)
	s.Require().Equal(msg.PeriodSeconds, params.SafetyMarginSeconds)

	h, err := s.Keeper.GetParamsUpdatedHeight(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(s.Ctx.BlockHeight(), h)

	_, err = s.MsgServer.InitialDkg(s.Ctx, msg)
	s.Require().ErrorIs(err, errInitialDkgAlreadySet)
}

func (s *KeeperSuite) TestMsgRegisterIdentity() {
	params := vrftypes.DefaultParams()
	params.ChainHash = []byte{0xaa}
	params.ReshareEpoch = 2
	s.Require().NoError(s.Keeper.SetParams(s.Ctx, params))

	_, _, addr := testdata.KeyTestPubAddr()
	operator := addr.String()
	valAddr := sdk.ValAddress(addr).String()

	msg := &vrftypes.MsgRegisterVrfIdentity{
		Operator:          operator,
		DrandBlsPublicKey: []byte("pk1"),
	}
	_, err := s.MsgServer.RegisterVrfIdentity(s.Ctx, msg)
	s.Require().NoError(err)

	identity, err := s.Keeper.identities.Get(s.Ctx, valAddr)
	s.Require().NoError(err)
	s.Require().Equal([]byte("pk1"), identity.DrandBlsPublicKey)
	s.Require().Equal(params.ChainHash, identity.ChainHash)
	s.Require().Equal(s.Ctx.BlockTime().Unix(), identity.SignalUnixSec)
	s.Require().Equal(params.ReshareEpoch, identity.SignalReshareEpoch)

	oldSignalTime := identity.SignalUnixSec
	oldSignalEpoch := identity.SignalReshareEpoch

	s.Ctx = s.Ctx.WithBlockTime(time.Unix(1700003000, 0).UTC())
	msg.DrandBlsPublicKey = []byte("pk2")
	_, err = s.MsgServer.RegisterVrfIdentity(s.Ctx, msg)
	s.Require().NoError(err)

	updated, err := s.Keeper.identities.Get(s.Ctx, valAddr)
	s.Require().NoError(err)
	s.Require().Equal([]byte("pk2"), updated.DrandBlsPublicKey)
	s.Require().Equal(oldSignalTime, updated.SignalUnixSec)
	s.Require().Equal(oldSignalEpoch, updated.SignalReshareEpoch)
}

func (s *KeeperSuite) TestMsgScheduleReshare() {
	_, _, addr := testdata.KeyTestPubAddr()
	scheduler := addr.String()

	params := vrftypes.DefaultParams()
	params.ReshareEpoch = 5
	s.Require().NoError(s.Keeper.SetParams(s.Ctx, params))

	msg := &vrftypes.MsgScheduleVrfReshare{
		Scheduler:    scheduler,
		ReshareEpoch: 6,
		Reason:       "test",
	}
	_, err := s.MsgServer.ScheduleVrfReshare(s.Ctx, msg)
	s.Require().ErrorIs(err, errSchedulerNotInCommittee)

	s.Require().NoError(s.Keeper.SetCommitteeMember(s.Ctx, scheduler, "sched"))
	msg.ReshareEpoch = 5
	_, err = s.MsgServer.ScheduleVrfReshare(s.Ctx, msg)
	s.Require().ErrorIs(err, errReshareEpochTooLow)

	msg.ReshareEpoch = 7
	_, err = s.MsgServer.ScheduleVrfReshare(s.Ctx, msg)
	s.Require().NoError(err)

	updated, err := s.Keeper.GetParams(s.Ctx)
	s.Require().NoError(err)
	s.Require().Equal(uint64(7), updated.ReshareEpoch)
}
