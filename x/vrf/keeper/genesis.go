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

	if err := k.SetParams(ctx, gs.Params); err != nil {
		panic(err)
	}
	_ = k.SetParamsUpdatedHeight(ctx, ctx.BlockHeight())

	if gs.LatestBeacon != nil {
		if err := k.SetLatestBeacon(ctx, *gs.LatestBeacon); err != nil {
			panic(err)
		}
	}

	for _, e := range gs.Committee {
		if err := k.SetCommitteeMember(ctx, e.Address, e.Label); err != nil {
			panic(err)
		}
	}

	// Ensure the module authority is always a committee member by default.
	if hasAuthority, err := k.committee.Has(ctx, k.GetAuthority()); err == nil && !hasAuthority {
		_ = k.SetCommitteeMember(ctx, k.GetAuthority(), "module_authority")
	}

	for _, i := range gs.Identities {
		if err := k.SetVrfIdentity(ctx, i); err != nil {
			panic(err)
		}
	}

	// Initialize last block time to the current block time so that ExtendVote can derive
	// Tref for the next height.
	_ = k.SetLastBlockTime(ctx, ctx.BlockTime().Unix())
}

// ExportGenesis exports the vrf module's state.
func (k Keeper) ExportGenesis(ctx sdk.Context) *types.GenesisState {
	params, err := k.GetParams(ctx)
	if err != nil {
		params = types.DefaultParams()
	}

	var beacon *types.VrfBeacon
	if b, err := k.GetLatestBeacon(ctx); err == nil {
		beacon = &b
	}

	var committee []types.AllowlistEntry
	_ = k.committee.Walk(ctx, nil, func(addr string, label string) (bool, error) {
		committee = append(committee, types.AllowlistEntry{Address: addr, Label: label})
		return false, nil
	})

	// Ensure the module authority is included in exported committee state even if
	// it was not explicitly stored.
	if hasAuthority, err := k.committee.Has(ctx, k.GetAuthority()); err == nil && !hasAuthority {
		committee = append(committee, types.AllowlistEntry{Address: k.GetAuthority(), Label: "module_authority"})
	}

	var identities []types.VrfIdentity
	_ = k.identities.Walk(ctx, nil, func(_ string, identity types.VrfIdentity) (bool, error) {
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
