package keeper

import (
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/vexxvakan/vrf/x/vrf/types"
)

var (
	errInvalidAuthority        = errors.New("vrf: invalid authority")
	errSchedulerNotInCommittee = errors.New("vrf: scheduler is not in committee")
	errInitiatorNotInCommittee = errors.New("vrf: initiator is not in committee")
	errInitialDkgAlreadySet    = errors.New("vrf: initial dkg already set")
	errReshareEpochTooLow      = errors.New("vrf: reshare_epoch must be > current")
)

type msgServer struct {
	k Keeper
}

// NewMsgServerImpl returns an implementation of types.MsgServer.
func NewMsgServerImpl(k Keeper) types.MsgServer {
	return &msgServer{k: k}
}

func (msgServer) VrfEmergencyDisable(
	ctx context.Context,
	msg *types.MsgVrfEmergencyDisable,
) (*types.MsgVrfEmergencyDisableResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	return &types.MsgVrfEmergencyDisableResponse{}, nil
}

func (s msgServer) InitialDkg(
	ctx context.Context,
	msg *types.MsgInitialDkg,
) (*types.MsgInitialDkgResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	allowed, err := s.k.IsCommitteeMember(ctx, msg.Initiator)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("%w: %s", errInitiatorNotInCommittee, msg.Initiator)
	}

	params, err := s.k.GetParams(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	if errors.Is(err, collections.ErrNotFound) {
		params = types.DefaultParams()
	}

	if params.ReshareEpoch != 0 || len(params.ChainHash) > 0 || len(params.PublicKey) > 0 || params.GenesisUnixSec != 0 {
		return nil, errInitialDkgAlreadySet
	}

	params.ChainHash = msg.ChainHash
	params.PublicKey = msg.PublicKey
	params.PeriodSeconds = msg.PeriodSeconds
	params.GenesisUnixSec = msg.GenesisUnixSec

	// Keep safety_margin_seconds valid if the period differs from defaults.
	if params.SafetyMarginSeconds < params.PeriodSeconds {
		params.SafetyMarginSeconds = params.PeriodSeconds
	}

	// The initial DKG corresponds to reshare_epoch=1.
	if params.ReshareEpoch == 0 {
		params.ReshareEpoch = 1
	}

	if err := s.k.SetParams(ctx, params); err != nil {
		return nil, err
	}
	if err := s.k.SetParamsUpdatedHeight(ctx, sdkCtx.BlockHeight()); err != nil {
		return nil, err
	}

	return &types.MsgInitialDkgResponse{}, nil
}

func (s msgServer) UpdateParams(
	ctx context.Context,
	msg *types.MsgUpdateParams,
) (*types.MsgUpdateParamsResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	if msg.Authority != s.k.GetAuthority() {
		return nil, fmt.Errorf("%w; expected %s, got %s", errInvalidAuthority, s.k.GetAuthority(), msg.Authority)
	}

	if err := s.k.SetParams(ctx, msg.Params); err != nil {
		return nil, err
	}
	if err := s.k.SetParamsUpdatedHeight(ctx, sdkCtx.BlockHeight()); err != nil {
		return nil, err
	}

	return &types.MsgUpdateParamsResponse{}, nil
}

func (s msgServer) AddVrfCommitteeMember(
	ctx context.Context,
	msg *types.MsgAddVrfCommitteeMember,
) (*types.MsgAddVrfCommitteeMemberResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	if msg.Authority != s.k.GetAuthority() {
		return nil, fmt.Errorf("%w; expected %s, got %s", errInvalidAuthority, s.k.GetAuthority(), msg.Authority)
	}

	if err := s.k.SetCommitteeMember(ctx, msg.Address, msg.Label); err != nil {
		return nil, err
	}

	return &types.MsgAddVrfCommitteeMemberResponse{}, nil
}

func (s msgServer) RemoveVrfCommitteeMember(
	ctx context.Context,
	msg *types.MsgRemoveVrfCommitteeMember,
) (*types.MsgRemoveVrfCommitteeMemberResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	if msg.Authority != s.k.GetAuthority() {
		return nil, fmt.Errorf("%w; expected %s, got %s", errInvalidAuthority, s.k.GetAuthority(), msg.Authority)
	}

	if err := s.k.RemoveCommitteeMember(ctx, msg.Address); err != nil {
		return nil, err
	}

	return &types.MsgRemoveVrfCommitteeMemberResponse{}, nil
}

func (s msgServer) RegisterVrfIdentity(
	ctx context.Context,
	msg *types.MsgRegisterVrfIdentity,
) (*types.MsgRegisterVrfIdentityResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	operatorAcc, err := sdk.AccAddressFromBech32(msg.Operator)
	if err != nil {
		return nil, fmt.Errorf("vrf: invalid operator address: %w", err)
	}

	validatorAddr := sdk.ValAddress(operatorAcc).String()

	params, err := s.k.GetParams(ctx)
	if err != nil {
		return nil, err
	}

	identity := types.VrfIdentity{
		ValidatorAddress:   validatorAddr,
		DrandBlsPublicKey:  msg.DrandBlsPublicKey,
		ChainHash:          params.ChainHash,
		SignalUnixSec:      sdkCtx.BlockTime().Unix(),
		SignalReshareEpoch: params.ReshareEpoch,
	}

	// If the identity already exists, preserve the original signal time and signal epoch.
	existing, err := s.k.identities.Get(ctx, validatorAddr)
	switch {
	case err == nil:
		identity.SignalUnixSec = existing.SignalUnixSec
		identity.SignalReshareEpoch = existing.SignalReshareEpoch
	case errors.Is(err, collections.ErrNotFound):
		// first registration; keep defaults above
	default:
		return nil, err
	}

	if err := s.k.SetVrfIdentity(ctx, identity); err != nil {
		return nil, err
	}

	return &types.MsgRegisterVrfIdentityResponse{}, nil
}

func (s msgServer) ScheduleVrfReshare(
	ctx context.Context,
	msg *types.MsgScheduleVrfReshare,
) (*types.MsgScheduleVrfReshareResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	allowed, err := s.k.IsCommitteeMember(ctx, msg.Scheduler)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("%w: %s", errSchedulerNotInCommittee, msg.Scheduler)
	}

	params, err := s.k.GetParams(ctx)
	if err != nil {
		return nil, err
	}

	if msg.ReshareEpoch <= params.ReshareEpoch {
		return nil, fmt.Errorf("%w: current=%d", errReshareEpochTooLow, params.ReshareEpoch)
	}

	params.ReshareEpoch = msg.ReshareEpoch
	if err := s.k.SetParams(ctx, params); err != nil {
		return nil, err
	}

	return &types.MsgScheduleVrfReshareResponse{}, nil
}
