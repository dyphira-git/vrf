package types

import (
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
)

// RegisterLegacyAminoCodec registers concrete types on the LegacyAmino codec.
func RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {
	cdc.RegisterConcrete(&MsgVrfEmergencyDisable{}, "vrf/MsgVrfEmergencyDisable", nil)
	cdc.RegisterConcrete(&MsgInitialDkg{}, "vrf/MsgInitialDkg", nil)
	cdc.RegisterConcrete(&MsgUpdateParams{}, "vrf/MsgUpdateParams", nil)
	cdc.RegisterConcrete(&MsgAddVrfCommitteeMember{}, "vrf/MsgAddVrfCommitteeMember", nil)
	cdc.RegisterConcrete(&MsgRemoveVrfCommitteeMember{}, "vrf/MsgRemoveVrfCommitteeMember", nil)
	cdc.RegisterConcrete(&MsgRegisterVrfIdentity{}, "vrf/MsgRegisterVrfIdentity", nil)
	cdc.RegisterConcrete(&MsgScheduleVrfReshare{}, "vrf/MsgScheduleVrfReshare", nil)
}

// RegisterInterfaces registers the module's interface types.
func RegisterInterfaces(registry types.InterfaceRegistry) {
	registry.RegisterImplementations(
		(*sdk.Msg)(nil),
		&MsgVrfEmergencyDisable{},
		&MsgInitialDkg{},
		&MsgUpdateParams{},
		&MsgAddVrfCommitteeMember{},
		&MsgRemoveVrfCommitteeMember{},
		&MsgRegisterVrfIdentity{},
		&MsgScheduleVrfReshare{},
	)

	msgservice.RegisterMsgServiceDesc(registry, &_Msg_serviceDesc)
}
