package keeper

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/vexxvakan/vrf/x/vrf/types"
)

const maxRandomWords = 256

var (
	errNilQueryRandomWordsRequest     = errors.New("vrf: nil QueryRandomWordsRequest")
	errRandomWordsCountMustBePositive = errors.New("vrf: count must be > 0")
	errRandomWordsCountExceedsMax     = errors.New("vrf: count exceeds maximum")
)

type queryServer struct {
	k Keeper
}

// NewQueryServerImpl returns an implementation of types.QueryServer.
func NewQueryServerImpl(k Keeper) types.QueryServer {
	return &queryServer{k: k}
}

func (q queryServer) Params(
	goCtx context.Context,
	_ *types.QueryParamsRequest,
) (*types.QueryParamsResponse, error) {
	params, err := q.k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}

	return &types.QueryParamsResponse{Params: params}, nil
}

func (q queryServer) Beacon(
	goCtx context.Context,
	_ *types.QueryBeaconRequest,
) (*types.QueryBeaconResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	beacon, err := q.k.GetBeacon(ctx)
	if err != nil {
		return nil, err
	}

	return &types.QueryBeaconResponse{Beacon: beacon}, nil
}

func (q queryServer) RandomWords(
	goCtx context.Context,
	req *types.QueryRandomWordsRequest,
) (*types.QueryRandomWordsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if req == nil {
		return nil, errNilQueryRandomWordsRequest
	}

	if req.Count == 0 {
		return nil, errRandomWordsCountMustBePositive
	}

	if req.Count > maxRandomWords {
		return nil, fmt.Errorf("%w: got %d, max %d", errRandomWordsCountExceedsMax, req.Count, maxRandomWords)
	}

	beacon, words, err := q.k.ExpandRandomness(ctx, req.Count, req.UserSeed)
	if err != nil {
		return nil, err
	}

	return &types.QueryRandomWordsResponse{
		DrandRound: beacon.DrandRound,
		Seed:       beacon.Randomness,
		Words:      words,
	}, nil
}
