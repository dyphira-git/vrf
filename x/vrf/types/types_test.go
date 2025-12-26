package types_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"

	vrftestutil "github.com/vexxvakan/vrf/x/vrf/testutil"
	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

type TypesSuite struct {
	vrftestutil.VrfTestSuite
}

func TestTypesSuite(t *testing.T) {
	suite.Run(t, new(TypesSuite))
}

func (s *TypesSuite) TestParamsValidate() {
	s.Require().NoError(vrftypes.DefaultParams().Validate())

	bad := vrftypes.VrfParams{PeriodSeconds: 0, SafetyMarginSeconds: 0}
	s.Require().Error(bad.Validate())

	bad = vrftypes.VrfParams{PeriodSeconds: 10, SafetyMarginSeconds: 5}
	s.Require().Error(bad.Validate())
}

func (s *TypesSuite) TestGenesisValidate() {
	_, _, addr := testdata.KeyTestPubAddr()
	member := addr.String()
	valAddr := sdk.ValAddress(addr).String()

	gs := vrftypes.GenesisState{
		Params:    vrftypes.DefaultParams(),
		Committee: []vrftypes.AllowlistEntry{{Address: member, Label: "member"}},
		Identities: []vrftypes.VrfIdentity{{
			ValidatorAddress:  valAddr,
			DrandBlsPublicKey: []byte("pk"),
		}},
	}
	s.Require().NoError(gs.Validate())

	gs.Committee = []vrftypes.AllowlistEntry{{Address: "", Label: "bad"}}
	s.Require().Error(gs.Validate())

	gs.Committee = []vrftypes.AllowlistEntry{{Address: "bad", Label: "bad"}}
	s.Require().Error(gs.Validate())

	gs = vrftypes.GenesisState{Params: vrftypes.DefaultParams()}
	gs.Identities = []vrftypes.VrfIdentity{{
		ValidatorAddress:  "",
		DrandBlsPublicKey: []byte("pk"),
	}}
	s.Require().Error(gs.Validate())

	gs.Identities = []vrftypes.VrfIdentity{{
		ValidatorAddress:  "bad",
		DrandBlsPublicKey: []byte("pk"),
	}}
	s.Require().Error(gs.Validate())

	gs.Identities = []vrftypes.VrfIdentity{{
		ValidatorAddress:  valAddr,
		DrandBlsPublicKey: nil,
	}}
	s.Require().Error(gs.Validate())

	gs = vrftypes.GenesisState{Params: vrftypes.DefaultParams()}
	gs.Params.ChainHash = []byte{0x01}
	gs.Identities = []vrftypes.VrfIdentity{{
		ValidatorAddress:  valAddr,
		DrandBlsPublicKey: []byte("pk"),
		ChainHash:         []byte{0x02},
	}}
	s.Require().Error(gs.Validate())
}

func (s *TypesSuite) TestMsgsValidate() {
	_, _, addr := testdata.KeyTestPubAddr()
	addrStr := addr.String()

	msg := &vrftypes.MsgVrfEmergencyDisable{Authority: addrStr}
	s.Require().NoError(msg.ValidateBasic())
	s.Require().Error((*vrftypes.MsgVrfEmergencyDisable)(nil).ValidateBasic())

	initMsg := &vrftypes.MsgInitialDkg{
		Initiator:      addrStr,
		ChainHash:      []byte{0x01},
		PublicKey:      []byte("pk"),
		PeriodSeconds:  1,
		GenesisUnixSec: 1,
	}
	s.Require().NoError(initMsg.ValidateBasic())
	initMsg.ChainHash = nil
	s.Require().Error(initMsg.ValidateBasic())

	params := vrftypes.DefaultParams()
	params.Enabled = true
	updateMsg := &vrftypes.MsgUpdateParams{Authority: addrStr, Params: params}
	s.Require().NoError(updateMsg.ValidateBasic())
	updateMsg.Authority = "bad"
	s.Require().Error(updateMsg.ValidateBasic())

	addMsg := &vrftypes.MsgAddVrfCommitteeMember{Authority: addrStr, Address: addrStr, Label: "l"}
	s.Require().NoError(addMsg.ValidateBasic())
	addMsg.Address = "bad"
	s.Require().Error(addMsg.ValidateBasic())

	removeMsg := &vrftypes.MsgRemoveVrfCommitteeMember{Authority: addrStr, Address: addrStr}
	s.Require().NoError(removeMsg.ValidateBasic())
	removeMsg.Authority = "bad"
	s.Require().Error(removeMsg.ValidateBasic())

	regMsg := &vrftypes.MsgRegisterVrfIdentity{Operator: addrStr, DrandBlsPublicKey: []byte("pk")}
	s.Require().NoError(regMsg.ValidateBasic())
	regMsg.DrandBlsPublicKey = nil
	s.Require().Error(regMsg.ValidateBasic())

	schedMsg := &vrftypes.MsgScheduleVrfReshare{Scheduler: addrStr, ReshareEpoch: 1}
	s.Require().NoError(schedMsg.ValidateBasic())
	schedMsg.ReshareEpoch = 0
	s.Require().Error(schedMsg.ValidateBasic())
}

func (s *TypesSuite) TestRoundAt() {
	params := vrftypes.DefaultParams()
	params.GenesisUnixSec = 100
	params.PeriodSeconds = 10

	genesis := time.Unix(100, 0).UTC()
	s.Require().Equal(uint64(1), vrftypes.RoundAt(params, genesis))
	s.Require().Equal(uint64(1), vrftypes.RoundAt(params, genesis.Add(9*time.Second)))
	s.Require().Equal(uint64(2), vrftypes.RoundAt(params, genesis.Add(10*time.Second)))

	params.PeriodSeconds = 0
	s.Require().Equal(uint64(0), vrftypes.RoundAt(params, genesis))

	params.PeriodSeconds = 10
	s.Require().Equal(uint64(0), vrftypes.RoundAt(params, genesis.Add(-time.Second)))
}
