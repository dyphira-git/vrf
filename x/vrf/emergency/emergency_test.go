package emergency

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"

	vrfkeeper "github.com/vexxvakan/vrf/x/vrf/keeper"
	vrftestutil "github.com/vexxvakan/vrf/x/vrf/testutil"
	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

type EmergencySuite struct {
	vrftestutil.VrfTestSuite

	Keeper *vrfkeeper.Keeper
}

func TestEmergencySuite(t *testing.T) {
	suite.Run(t, new(EmergencySuite))
}

func (s *EmergencySuite) SetupTest() {
	s.VrfTestSuite.SetupTest()

	k := vrfkeeper.NewKeeper(runtime.NewKVStoreService(s.KeyVrf), s.EncCfg.Codec, s.Authority)
	s.Keeper = &k
}

func (s *EmergencySuite) TestVerifyEmergencyMsg() {
	msg := &vrftypes.MsgVrfEmergencyDisable{Authority: s.Addr.String(), Reason: "now"}
	txSigned, err := s.BuildSignedTx(msg)
	s.Require().NoError(err)

	found, authorized, reason, err := VerifyEmergencyMsg(s.Ctx, txSigned, s.AccountKeeper, s.Keeper, nil)
	s.Require().ErrorIs(err, errNilSignModeHandler)
	s.Require().False(found)
	s.Require().False(authorized)
	s.Require().Empty(reason)

	found, authorized, reason, err = VerifyEmergencyMsg(s.Ctx, txSigned, s.AccountKeeper, s.Keeper, s.SignModeHandler)
	s.Require().NoError(err)
	s.Require().True(found)
	s.Require().False(authorized)
	s.Require().Empty(reason)

	s.Require().NoError(s.Keeper.SetCommitteeMember(s.Ctx, s.Addr.String(), "member"))
	found, authorized, reason, err = VerifyEmergencyMsg(s.Ctx, txSigned, s.AccountKeeper, s.Keeper, s.SignModeHandler)
	s.Require().NoError(err)
	s.Require().True(found)
	s.Require().True(authorized)
	s.Require().Equal("now", reason)
}

func (s *EmergencySuite) TestVerifyEmergencyMsgNoEmergency() {
	params := vrftypes.DefaultParams()
	msg := &vrftypes.MsgUpdateParams{Authority: s.Addr.String(), Params: params}
	txSigned, err := s.BuildSignedTx(msg)
	s.Require().NoError(err)

	found, authorized, reason, err := VerifyEmergencyMsg(s.Ctx, txSigned, s.AccountKeeper, s.Keeper, s.SignModeHandler)
	s.Require().NoError(err)
	s.Require().False(found)
	s.Require().False(authorized)
	s.Require().Empty(reason)
}

func (s *EmergencySuite) TestVerifyEmergencyMsgMixedMsgs() {
	params := vrftypes.DefaultParams()
	msg1 := &vrftypes.MsgVrfEmergencyDisable{Authority: s.Addr.String()}
	msg2 := &vrftypes.MsgUpdateParams{Authority: s.Addr.String(), Params: params}
	txSigned, err := s.BuildSignedTx(msg1, msg2)
	s.Require().NoError(err)

	found, authorized, _, err := VerifyEmergencyMsg(s.Ctx, txSigned, s.AccountKeeper, s.Keeper, s.SignModeHandler)
	s.Require().ErrorIs(err, errEmergencyDisableTxNotDedicated)
	s.Require().True(found)
	s.Require().False(authorized)
}

func (s *EmergencySuite) TestVerifyEmergencyMsgAccountMissing() {
	priv, _, addr := testdata.KeyTestPubAddr()
	msg := &vrftypes.MsgVrfEmergencyDisable{Authority: addr.String()}

	builder := s.EncCfg.TxConfig.NewTxBuilder()
	s.Require().NoError(builder.SetMsgs(msg))

	sig := signing.SignatureV2{
		PubKey: priv.PubKey(),
		Data: &signing.SingleSignatureData{
			SignMode: signing.SignMode_SIGN_MODE_DIRECT,
		},
		Sequence: 0,
	}
	s.Require().NoError(builder.SetSignatures(sig))

	signerData := authsigning.SignerData{
		Address:       addr.String(),
		ChainID:       s.Ctx.ChainID(),
		AccountNumber: 1,
		Sequence:      0,
		PubKey:        priv.PubKey(),
	}

	sigV2, err := tx.SignWithPrivKey(
		s.Ctx,
		signing.SignMode_SIGN_MODE_DIRECT,
		signerData,
		builder,
		priv,
		s.EncCfg.TxConfig,
		0,
	)
	s.Require().NoError(err)
	s.Require().NoError(builder.SetSignatures(sigV2))

	txSigned := builder.GetTx()
	_, _, _, err = VerifyEmergencyMsg(s.Ctx, txSigned, s.AccountKeeper, s.Keeper, s.SignModeHandler)
	s.Require().ErrorIs(err, errSignerAccountNotFound)
}
