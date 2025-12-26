package ante

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"

	vrfkeeper "github.com/vexxvakan/vrf/x/vrf/keeper"
	vrftestutil "github.com/vexxvakan/vrf/x/vrf/testutil"
	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

type EmergencyDecoratorSuite struct {
	vrftestutil.VrfTestSuite

	Keeper *vrfkeeper.Keeper
}

func TestEmergencyDecoratorSuite(t *testing.T) {
	suite.Run(t, new(EmergencyDecoratorSuite))
}

func (s *EmergencyDecoratorSuite) SetupTest() {
	s.VrfTestSuite.SetupTest()

	k := vrfkeeper.NewKeeper(runtime.NewKVStoreService(s.KeyVrf), s.EncCfg.Codec, s.Authority)
	s.Keeper = &k
}

func (s *EmergencyDecoratorSuite) TestSimulateBypass() {
	decorator := NewEmergencyDisableDecorator(s.AccountKeeper, s.Keeper, s.SignModeHandler)
	msg := &vrftypes.MsgVrfEmergencyDisable{Authority: s.Addr.String()}
	txSigned, err := s.BuildSignedTx(msg)
	s.Require().NoError(err)

	called := false
	next := func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) {
		called = true
		return ctx, nil
	}

	_, err = decorator.AnteHandle(s.Ctx, txSigned, true, next)
	s.Require().NoError(err)
	s.Require().True(called)
}

func (s *EmergencyDecoratorSuite) TestNoEmergency() {
	decorator := NewEmergencyDisableDecorator(s.AccountKeeper, s.Keeper, s.SignModeHandler)
	params := vrftypes.DefaultParams()
	msg := &vrftypes.MsgUpdateParams{Authority: s.Addr.String(), Params: params}
	txSigned, err := s.BuildSignedTx(msg)
	s.Require().NoError(err)

	called := false
	next := func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) {
		called = true
		return ctx, nil
	}

	_, err = decorator.AnteHandle(s.Ctx, txSigned, false, next)
	s.Require().NoError(err)
	s.Require().True(called)
}

func (s *EmergencyDecoratorSuite) TestUnauthorized() {
	decorator := NewEmergencyDisableDecorator(s.AccountKeeper, s.Keeper, s.SignModeHandler)
	msg := &vrftypes.MsgVrfEmergencyDisable{Authority: s.Addr.String()}
	txSigned, err := s.BuildSignedTx(msg)
	s.Require().NoError(err)

	called := false
	next := func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) {
		called = true
		return ctx, nil
	}

	_, err = decorator.AnteHandle(s.Ctx, txSigned, false, next)
	s.Require().ErrorIs(err, errUnauthorizedMsgVrfEmergencyDisable)
	s.Require().False(called)
}

func (s *EmergencyDecoratorSuite) TestAuthorized() {
	decorator := NewEmergencyDisableDecorator(s.AccountKeeper, s.Keeper, s.SignModeHandler)
	msg := &vrftypes.MsgVrfEmergencyDisable{Authority: s.Addr.String()}
	txSigned, err := s.BuildSignedTx(msg)
	s.Require().NoError(err)

	s.Require().NoError(s.Keeper.SetCommitteeMember(s.Ctx, s.Addr.String(), "member"))

	called := false
	next := func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) {
		called = true
		return ctx, nil
	}

	_, err = decorator.AnteHandle(s.Ctx, txSigned, false, next)
	s.Require().NoError(err)
	s.Require().False(called)
}
