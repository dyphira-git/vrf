package vrf

import (
	"errors"
	"testing"
	"time"

	cmtabci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/drand/drand/v2/crypto"
	"github.com/stretchr/testify/suite"

	"cosmossdk.io/collections"
	"cosmossdk.io/log"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/types/module"

	abcicodec "github.com/vexxvakan/vrf/x/vrf/abci/codec"
	vrfkeeper "github.com/vexxvakan/vrf/x/vrf/keeper"
	vrftestutil "github.com/vexxvakan/vrf/x/vrf/testutil"
	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

type PreBlockSuite struct {
	vrftestutil.VrfTestSuite

	keeper  vrfkeeper.Keeper
	handler *PreBlockHandler
}

func TestPreBlockSuite(t *testing.T) {
	suite.Run(t, new(PreBlockSuite))
}

func (s *PreBlockSuite) SetupTest() {
	s.VrfTestSuite.SetupTest()

	k := vrfkeeper.NewKeeper(runtime.NewKVStoreService(s.KeyVrf), s.EncCfg.Codec, s.Authority)

	// Minimal params required for the PreBlock BLS/public-key verification setup.
	scheme := crypto.NewPedersenBLSChained()
	pubKey := scheme.KeyGroup.Point().Mul(scheme.KeyGroup.Scalar().SetInt64(1), nil)
	pubKeyBz, err := pubKey.MarshalBinary()
	s.Require().NoError(err)

	params := vrftypes.DefaultParams()
	params.Enabled = true
	params.GenesisUnixSec = s.Ctx.BlockTime().Unix() - 100
	params.PeriodSeconds = 2
	params.SafetyMarginSeconds = 2
	params.PublicKey = pubKeyBz
	s.Require().NoError(k.SetParams(s.Ctx, params))

	s.keeper = k

	s.handler = NewPreBlockHandler(
		log.NewNopLogger(),
		&s.keeper,
		s.AccountKeeper,
		s.SignModeHandler,
		s.EncCfg.TxConfig.TxDecoder(),
	)
}

func (s *PreBlockSuite) TestWrappedPreBlocker_RejectsBlockWhenInsufficientVotingPower() {
	ctx := s.Ctx.
		WithBlockHeight(10).
		WithBlockTime(time.Unix(1700000010, 0).UTC()).
		WithConsensusParams(cmtproto.ConsensusParams{
			Abci: &cmtproto.ABCIParams{VoteExtensionsEnableHeight: 1},
		})

	// Seed a prior time so the target drand round is available.
	s.Require().NoError(s.keeper.SetLastBlockTime(ctx, 1700000000))

	// Inject an extended commit info payload with no VRF vote extensions at all.
	extCommit := cmtabci.ExtendedCommitInfo{
		Round: 0,
		Votes: []cmtabci.ExtendedVoteInfo{
			{
				Validator:     cmtabci.Validator{Address: make([]byte, 20), Power: 334},
				BlockIdFlag:   cmtproto.BlockIDFlagCommit,
				VoteExtension: nil,
			},
			{
				Validator:     cmtabci.Validator{Address: make([]byte, 20), Power: 166},
				BlockIdFlag:   cmtproto.BlockIDFlagCommit,
				VoteExtension: nil,
			},
		},
	}

	extCommitCodec := abcicodec.NewCompressionExtendedCommitCodec(
		abcicodec.NewDefaultExtendedCommitCodec(),
		abcicodec.NewZStdCompressor(),
	)
	extCommitBz, err := extCommitCodec.Encode(extCommit)
	s.Require().NoError(err)

	req := &cmtabci.RequestFinalizeBlock{
		Height: ctx.BlockHeight(),
		Txs:    [][]byte{extCommitBz},
	}

	mm := module.NewManager()
	_, err = s.handler.WrappedPreBlocker(mm)(ctx, req)
	s.Require().Error(err)
	s.Require().True(errors.Is(err, errInsufficientVotingPowerForValidBeacons))

	// No beacon is written, and block time is not advanced (block must be rejected).
	_, err = s.keeper.GetLatestBeacon(ctx)
	s.Require().True(errors.Is(err, collections.ErrNotFound))

	last, err := s.keeper.GetLastBlockTime(ctx)
	s.Require().NoError(err)
	s.Require().Equal(int64(1700000000), last)
}
