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
	goCtx context.Context,
	msg *types.MsgVrfEmergencyDisable,
) (*types.MsgVrfEmergencyDisableResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"vrf_emergency_disable",
			sdk.NewAttribute("authority", msg.Authority),
			sdk.NewAttribute("reason", msg.Reason),
		),
	)

	return &types.MsgVrfEmergencyDisableResponse{}, nil
}

func (s msgServer) UpdateParams(
	goCtx context.Context,
	msg *types.MsgUpdateParams,
) (*types.MsgUpdateParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	if msg.Authority != s.k.GetAuthority() {
		return nil, fmt.Errorf("%w; expected %s, got %s", errInvalidAuthority, s.k.GetAuthority(), msg.Authority)
	}

	if err := s.k.SetParams(goCtx, msg.Params); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"vrf_update_params",
			sdk.NewAttribute("authority", msg.Authority),
		),
	)

	return &types.MsgUpdateParamsResponse{}, nil
}

func (s msgServer) AddVrfCommitteeMember(
	goCtx context.Context,
	msg *types.MsgAddVrfCommitteeMember,
) (*types.MsgAddVrfCommitteeMemberResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	if msg.Authority != s.k.GetAuthority() {
		return nil, fmt.Errorf("%w; expected %s, got %s", errInvalidAuthority, s.k.GetAuthority(), msg.Authority)
	}

	if err := s.k.SetCommitteeMember(goCtx, msg.Address, msg.Label); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"vrf_add_committee_member",
			sdk.NewAttribute("authority", msg.Authority),
			sdk.NewAttribute("address", msg.Address),
			sdk.NewAttribute("label", msg.Label),
		),
	)

	return &types.MsgAddVrfCommitteeMemberResponse{}, nil
}

func (s msgServer) RemoveVrfCommitteeMember(
	goCtx context.Context,
	msg *types.MsgRemoveVrfCommitteeMember,
) (*types.MsgRemoveVrfCommitteeMemberResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	if msg.Authority != s.k.GetAuthority() {
		return nil, fmt.Errorf("%w; expected %s, got %s", errInvalidAuthority, s.k.GetAuthority(), msg.Authority)
	}

	if err := s.k.RemoveCommitteeMember(goCtx, msg.Address); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"vrf_remove_committee_member",
			sdk.NewAttribute("authority", msg.Authority),
			sdk.NewAttribute("address", msg.Address),
		),
	)

	return &types.MsgRemoveVrfCommitteeMemberResponse{}, nil
}

func (s msgServer) RegisterVrfIdentity(
	goCtx context.Context,
	msg *types.MsgRegisterVrfIdentity,
) (*types.MsgRegisterVrfIdentityResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	operatorAcc, err := sdk.AccAddressFromBech32(msg.Operator)
	if err != nil {
		return nil, fmt.Errorf("vrf: invalid operator address: %w", err)
	}

	validatorAddr := sdk.ValAddress(operatorAcc).String()

	params, err := s.k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}

	identity := types.VrfIdentity{
		ValidatorAddress:   validatorAddr,
		DrandBlsPublicKey:  msg.DrandBlsPublicKey,
		ChainHash:          params.ChainHash,
		SignalUnixSec:      ctx.BlockTime().Unix(),
		SignalReshareEpoch: params.ReshareEpoch,
	}

	// If the identity already exists, preserve the original signal time and signal epoch.
	existing, err := s.k.identities.Get(goCtx, validatorAddr)
	switch {
	case err == nil:
		identity.SignalUnixSec = existing.SignalUnixSec
		identity.SignalReshareEpoch = existing.SignalReshareEpoch
	case errors.Is(err, collections.ErrNotFound):
		// first registration; keep defaults above
	default:
		return nil, err
	}

	if err := s.k.SetVrfIdentity(goCtx, identity); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"vrf_register_identity",
			sdk.NewAttribute("operator", msg.Operator),
			sdk.NewAttribute("validator_address", validatorAddr),
		),
	)

	return &types.MsgRegisterVrfIdentityResponse{}, nil
}

func (s msgServer) ScheduleVrfReshare(
	goCtx context.Context,
	msg *types.MsgScheduleVrfReshare,
) (*types.MsgScheduleVrfReshareResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	allowed, err := s.k.IsCommitteeMember(goCtx, msg.Scheduler)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("%w: %s", errSchedulerNotInCommittee, msg.Scheduler)
	}

	params, err := s.k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}

	if msg.ReshareEpoch <= params.ReshareEpoch {
		return nil, fmt.Errorf("%w: current=%d", errReshareEpochTooLow, params.ReshareEpoch)
	}

	oldEpoch := params.ReshareEpoch
	params.ReshareEpoch = msg.ReshareEpoch
	if err := s.k.SetParams(goCtx, params); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"vrf_schedule_reshare",
			sdk.NewAttribute("scheduler", msg.Scheduler),
			sdk.NewAttribute("old_reshare_epoch", fmt.Sprintf("%d", oldEpoch)),
			sdk.NewAttribute("new_reshare_epoch", fmt.Sprintf("%d", msg.ReshareEpoch)),
			sdk.NewAttribute("reason", msg.Reason),
		),
	)

	return &types.MsgScheduleVrfReshareResponse{}, nil
}
