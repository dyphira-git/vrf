package module

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"
)

func (AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: "vexxvakan.vrf.v1.Query",
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "Params",
					Use:       "params",
					Short:     "Query the current VRF parameters",
				},
				{
					RpcMethod: "Beacon",
					Use:       "beacon",
					Short:     "Query the latest VRF beacon",
				},
				{
					RpcMethod: "RandomWords",
					Use:       "random-words",
					Short:     "Expand the latest beacon into random words",
				},
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service:              "vexxvakan.vrf.v1.Msg",
			EnhanceCustomCommand: true,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "UpdateParams",
					Skip:      true, // authority gated
				},
				{
					RpcMethod: "AddVrfCommitteeMember",
					Skip:      true, // authority gated
				},
				{
					RpcMethod: "RemoveVrfCommitteeMember",
					Skip:      true, // authority gated
				},
				{
					RpcMethod: "VrfEmergencyDisable",
					Use:       "emergency-disable",
					Short:     "Submit a VRF emergency disable request",
				},
				{
					RpcMethod: "InitialDkg",
					Use:       "initial-dkg",
					Short:     "Set initial drand chain-info for VRF bootstrap",
				},
				{
					RpcMethod: "RegisterVrfIdentity",
					Use:       "register-identity",
					Short:     "Register the operator's drand BLS public key",
				},
				{
					RpcMethod: "ScheduleVrfReshare",
					Use:       "schedule-reshare",
					Short:     "Schedule a VRF reshare by bumping reshare_epoch",
				},
			},
		},
	}
}
