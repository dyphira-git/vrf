package ve

import (
	"testing"
	"time"

	cometabci "github.com/cometbft/cometbft/abci/types"
	"github.com/stretchr/testify/suite"

	"cosmossdk.io/log"

	"github.com/cosmos/cosmos-sdk/runtime"

	vrfkeeper "github.com/vexxvakan/vrf/x/vrf/keeper"
	vrfclient "github.com/vexxvakan/vrf/x/vrf/sidecar"
	vrftestutil "github.com/vexxvakan/vrf/x/vrf/testutil"
	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

type VoteExtensionHandlerSuite struct {
	vrftestutil.VrfTestSuite

	keeper  vrfkeeper.Keeper
	handler *Handler
}

func TestVoteExtensionHandlerSuite(t *testing.T) {
	suite.Run(t, new(VoteExtensionHandlerSuite))
}

func (s *VoteExtensionHandlerSuite) SetupTest() {
	s.VrfTestSuite.SetupTest()

	k := vrfkeeper.NewKeeper(runtime.NewKVStoreService(s.KeyVrf), s.EncCfg.Codec, s.Authority)
	s.Require().NoError(k.SetParams(s.Ctx, vrftypes.DefaultParams()))

	s.keeper = k
	s.handler = NewHandler(log.NewNopLogger(), vrfclient.NoOpClient{}, &s.keeper, time.Second)
}

func (s *VoteExtensionHandlerSuite) TestVerifyVoteExtension_EmptyAcceptedWhenDisabled() {
	resp, err := s.handler.VerifyVoteExtensionHandler()(s.Ctx, &cometabci.RequestVerifyVoteExtension{
		Height:        10,
		VoteExtension: nil,
	})
	s.Require().NoError(err)
	s.Require().Equal(cometabci.ResponseVerifyVoteExtension_ACCEPT, resp.Status)
}

func (s *VoteExtensionHandlerSuite) TestVerifyVoteExtension_EmptyRejectedWhenEnabled() {
	params := vrftypes.DefaultParams()
	params.Enabled = true
	s.Require().NoError(s.keeper.SetParams(s.Ctx, params))

	resp, err := s.handler.VerifyVoteExtensionHandler()(s.Ctx, &cometabci.RequestVerifyVoteExtension{
		Height:        10,
		VoteExtension: nil,
	})
	s.Require().NoError(err)
	s.Require().Equal(cometabci.ResponseVerifyVoteExtension_REJECT, resp.Status)
}
