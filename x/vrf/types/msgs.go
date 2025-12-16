package types

import (
	"errors"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

var (
	errMsgVrfEmergencyDisableNil            = errors.New("MsgVrfEmergencyDisable: message cannot be nil")
	errMsgUpdateParamsNil                   = errors.New("MsgUpdateParams: message cannot be nil")
	errMsgAddVrfCommitteeMemberNil          = errors.New("MsgAddVrfCommitteeMember: message cannot be nil")
	errMsgRemoveVrfCommitteeMemberNil       = errors.New("MsgRemoveVrfCommitteeMember: message cannot be nil")
	errMsgRegisterVrfIdentityNil            = errors.New("MsgRegisterVrfIdentity: message cannot be nil")
	errMsgRegisterVrfIdentityPublicKeyEmpty = errors.New("MsgRegisterVrfIdentity: drand_bls_public_key must not be empty")
	errMsgScheduleVrfReshareNil             = errors.New("MsgScheduleVrfReshare: message cannot be nil")
	errMsgScheduleVrfReshareEpochZero       = errors.New("MsgScheduleVrfReshare: reshare_epoch must be > 0")
)

func (m *MsgVrfEmergencyDisable) ValidateBasic() error {
	if m == nil {
		return errMsgVrfEmergencyDisableNil
	}

	if _, err := sdk.AccAddressFromBech32(m.Authority); err != nil {
		return fmt.Errorf("MsgVrfEmergencyDisable: invalid authority address: %w", err)
	}

	// Empty reason is allowed.
	return nil
}

func (m *MsgUpdateParams) ValidateBasic() error {
	if m == nil {
		return errMsgUpdateParamsNil
	}

	if _, err := sdk.AccAddressFromBech32(m.Authority); err != nil {
		return fmt.Errorf("MsgUpdateParams: invalid authority address: %w", err)
	}

	if err := m.Params.Validate(); err != nil {
		return fmt.Errorf("MsgUpdateParams: invalid params: %w", err)
	}

	return nil
}

func (m *MsgAddVrfCommitteeMember) ValidateBasic() error {
	if m == nil {
		return errMsgAddVrfCommitteeMemberNil
	}

	if _, err := sdk.AccAddressFromBech32(m.Authority); err != nil {
		return fmt.Errorf("MsgAddVrfCommitteeMember: invalid authority address: %w", err)
	}

	if _, err := sdk.AccAddressFromBech32(m.Address); err != nil {
		return fmt.Errorf("MsgAddVrfCommitteeMember: invalid address: %w", err)
	}

	return nil
}

func (m *MsgRemoveVrfCommitteeMember) ValidateBasic() error {
	if m == nil {
		return errMsgRemoveVrfCommitteeMemberNil
	}

	if _, err := sdk.AccAddressFromBech32(m.Authority); err != nil {
		return fmt.Errorf("MsgRemoveVrfCommitteeMember: invalid authority address: %w", err)
	}

	if _, err := sdk.AccAddressFromBech32(m.Address); err != nil {
		return fmt.Errorf("MsgRemoveVrfCommitteeMember: invalid address: %w", err)
	}

	return nil
}

func (m *MsgRegisterVrfIdentity) ValidateBasic() error {
	if m == nil {
		return errMsgRegisterVrfIdentityNil
	}

	if _, err := sdk.AccAddressFromBech32(m.Operator); err != nil {
		return fmt.Errorf("MsgRegisterVrfIdentity: invalid operator: %w", err)
	}

	if len(m.DrandBlsPublicKey) == 0 {
		return errMsgRegisterVrfIdentityPublicKeyEmpty
	}

	return nil
}

func (m *MsgScheduleVrfReshare) ValidateBasic() error {
	if m == nil {
		return errMsgScheduleVrfReshareNil
	}

	if _, err := sdk.AccAddressFromBech32(m.Scheduler); err != nil {
		return fmt.Errorf("MsgScheduleVrfReshare: invalid scheduler address: %w", err)
	}

	if m.ReshareEpoch == 0 {
		return errMsgScheduleVrfReshareEpochZero
	}

	return nil
}
