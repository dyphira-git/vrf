package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/store"

	"github.com/cosmos/cosmos-sdk/codec"

	"github.com/vexxvakan/vrf/x/vrf/types"
)

var errCannotRemoveModuleAuthority = errors.New("vrf: cannot remove module authority from committee")

type Keeper struct {
	storeService store.KVStoreService
	cdc          codec.BinaryCodec

	authority string

	schema              collections.Schema
	params              collections.Item[types.VrfParams]
	latestBeacon        collections.Item[types.VrfBeacon]
	lastBlockTime       collections.Item[int64]
	prevBlockTime       collections.Item[int64]
	paramsUpdatedHeight collections.Item[int64]

	committee  collections.Map[string, string]
	identities collections.Map[string, types.VrfIdentity]
}

func NewKeeper(
	ss store.KVStoreService,
	cdc codec.BinaryCodec,
	authority string,
) Keeper {
	sb := collections.NewSchemaBuilder(ss)

	k := Keeper{
		storeService:        ss,
		cdc:                 cdc,
		authority:           authority,
		params:              collections.NewItem(sb, collections.NewPrefix(0), "params", codec.CollValue[types.VrfParams](cdc)),
		latestBeacon:        collections.NewItem(sb, collections.NewPrefix(1), "latest_beacon", codec.CollValue[types.VrfBeacon](cdc)),
		lastBlockTime:       collections.NewItem(sb, collections.NewPrefix(2), "last_block_time", collections.Int64Value),
		prevBlockTime:       collections.NewItem(sb, collections.NewPrefix(6), "prev_block_time", collections.Int64Value),
		paramsUpdatedHeight: collections.NewItem(sb, collections.NewPrefix(4), "params_updated_height", collections.Int64Value),
		committee:           collections.NewMap(sb, collections.NewPrefix(3), "vrf_committee", collections.StringKey, collections.StringValue),
		identities:          collections.NewMap(sb, collections.NewPrefix(5), "vrf_identities", collections.StringKey, codec.CollValue[types.VrfIdentity](cdc)),
	}

	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}

	k.schema = schema

	return k
}

func (k Keeper) Schema() collections.Schema {
	return k.schema
}

func (k Keeper) GetAuthority() string {
	return k.authority
}

func (k Keeper) GetParams(ctx context.Context) (types.VrfParams, error) {
	return k.params.Get(ctx)
}

func (k Keeper) SetParams(ctx context.Context, p types.VrfParams) error {
	if err := p.Validate(); err != nil {
		return err
	}

	return k.params.Set(ctx, p)
}

func (k Keeper) GetLatestBeacon(ctx context.Context) (types.VrfBeacon, error) {
	return k.latestBeacon.Get(ctx)
}

func (k Keeper) SetLatestBeacon(ctx context.Context, b types.VrfBeacon) error {
	return k.latestBeacon.Set(ctx, b)
}

func (k Keeper) GetLastBlockTime(ctx context.Context) (int64, error) {
	return k.lastBlockTime.Get(ctx)
}

func (k Keeper) SetLastBlockTime(ctx context.Context, ts int64) error {
	prev, err := k.lastBlockTime.Get(ctx)
	if err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			return err
		}
		prev = ts
	}
	if err := k.prevBlockTime.Set(ctx, prev); err != nil {
		return err
	}
	return k.lastBlockTime.Set(ctx, ts)
}

func (k Keeper) GetPrevBlockTime(ctx context.Context) (int64, error) {
	ts, err := k.prevBlockTime.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return ts, nil
}

func (k Keeper) GetParamsUpdatedHeight(ctx context.Context) (int64, error) {
	height, err := k.paramsUpdatedHeight.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return height, nil
}

func (k Keeper) SetParamsUpdatedHeight(ctx context.Context, height int64) error {
	return k.paramsUpdatedHeight.Set(ctx, height)
}

// SetCommitteeMember adds or updates a committee member in the on-chain allowlist.
func (k Keeper) SetCommitteeMember(ctx context.Context, addr string, label string) error {
	return k.committee.Set(ctx, addr, label)
}

// RemoveCommitteeMember removes a committee member from the allowlist. The module
// authority can never be removed.
func (k Keeper) RemoveCommitteeMember(ctx context.Context, addr string) error {
	if addr == k.authority {
		return errCannotRemoveModuleAuthority
	}
	return k.committee.Remove(ctx, addr)
}

// IsCommitteeMember returns true if the given bech32 address string is present
// in the committee allowlist, or if it is the module authority.
func (k Keeper) IsCommitteeMember(ctx context.Context, addr string) (bool, error) {
	if addr == k.authority {
		return true, nil
	}
	return k.committee.Has(ctx, addr)
}

// SetVrfIdentity upserts a validator's VRF identity binding.
func (k Keeper) SetVrfIdentity(ctx context.Context, identity types.VrfIdentity) error {
	return k.identities.Set(ctx, identity.ValidatorAddress, identity)
}

// RemoveVrfIdentity removes a validator's VRF identity binding.
func (k Keeper) RemoveVrfIdentity(ctx context.Context, validatorAddr string) error {
	return k.identities.Remove(ctx, validatorAddr)
}
