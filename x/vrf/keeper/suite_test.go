package keeper

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/cosmos/cosmos-sdk/runtime"

	vrftestutil "github.com/vexxvakan/vrf/x/vrf/testutil"
	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

type KeeperSuite struct {
	vrftestutil.VrfTestSuite

	Keeper      Keeper
	MsgServer   vrftypes.MsgServer
	QueryServer vrftypes.QueryServer
}

func TestKeeperSuite(t *testing.T) {
	suite.Run(t, new(KeeperSuite))
}

func (s *KeeperSuite) SetupTest() {
	s.VrfTestSuite.SetupTest()

	k := NewKeeper(runtime.NewKVStoreService(s.KeyVrf), s.EncCfg.Codec, s.Authority)
	s.Require().NoError(k.SetParams(s.Ctx, vrftypes.DefaultParams()))

	s.Keeper = k
	s.MsgServer = NewMsgServerImpl(k)
	s.QueryServer = NewQueryServerImpl(k)
}
