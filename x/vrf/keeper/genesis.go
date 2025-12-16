package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/vexxvakan/vrf/x/vrf/types"
)

// InitGenesis initializes the vrf module's state from genesis.
func (k Keeper) InitGenesis(ctx sdk.Context, gs types.GenesisState) {
	if err := gs.Validate(); err != nil {
		panic(err)
	}

	storeCtx := sdk.WrapSDKContext(ctx)

	if err := k.SetParams(storeCtx, gs.Params); err != nil {
		panic(err)
	}

	if gs.LatestBeacon != nil {
		if err := k.SetLatestBeacon(storeCtx, *gs.LatestBeacon); err != nil {
			panic(err)
		}
	}

	for _, e := range gs.Committee {
		if err := k.SetCommitteeMember(storeCtx, e.Address, e.Label); err != nil {
			panic(err)
		}
	}

	// Ensure the module authority is always a committee member by default.
	if hasAuthority, err := k.committee.Has(storeCtx, k.GetAuthority()); err == nil && !hasAuthority {
		_ = k.SetCommitteeMember(storeCtx, k.GetAuthority(), "module_authority")
	}

	for _, i := range gs.Identities {
		if err := k.SetVrfIdentity(storeCtx, i); err != nil {
			panic(err)
		}
	}

	// Initialize last block time to the current block time so that ExtendVote can derive
	// Tref for the next height.
	_ = k.SetLastBlockTime(storeCtx, ctx.BlockTime().Unix())
}

// ExportGenesis exports the vrf module's state.
func (k Keeper) ExportGenesis(ctx sdk.Context) *types.GenesisState {
	storeCtx := sdk.WrapSDKContext(ctx)

	params, err := k.GetParams(storeCtx)
	if err != nil {
		params = types.DefaultParams()
	}

	var beacon *types.VrfBeacon
	if b, err := k.GetLatestBeacon(storeCtx); err == nil {
		beacon = &b
	}

	var committee []types.AllowlistEntry
	_ = k.committee.Walk(storeCtx, nil, func(addr string, label string) (bool, error) {
		committee = append(committee, types.AllowlistEntry{Address: addr, Label: label})
		return false, nil
	})

	// Ensure the module authority is included in exported committee state even if
	// it was not explicitly stored.
	if hasAuthority, err := k.committee.Has(storeCtx, k.GetAuthority()); err == nil && !hasAuthority {
		committee = append(committee, types.AllowlistEntry{Address: k.GetAuthority(), Label: "module_authority"})
	}

	var identities []types.VrfIdentity
	_ = k.identities.Walk(storeCtx, nil, func(_ string, identity types.VrfIdentity) (bool, error) {
		identities = append(identities, identity)
		return false, nil
	})

	return &types.GenesisState{
		Params:       params,
		LatestBeacon: beacon,
		Committee:    committee,
		Identities:   identities,
	}
}
