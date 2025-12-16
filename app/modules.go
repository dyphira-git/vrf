package app

import (
	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/x/upgrade"
	upgradetypes "cosmossdk.io/x/upgrade/types"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/auth"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/auth/vesting"
	vestingtypes "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	"github.com/cosmos/cosmos-sdk/x/bank"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/cosmos-sdk/x/consensus"
	consensusparamtypes "github.com/cosmos/cosmos-sdk/x/consensus/types"
	distr "github.com/cosmos/cosmos-sdk/x/distribution"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	"github.com/cosmos/cosmos-sdk/x/genutil"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
	"github.com/cosmos/cosmos-sdk/x/gov"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	minttypes "github.com/cosmos/cosmos-sdk/x/mint/types"
	"github.com/cosmos/cosmos-sdk/x/slashing"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	"github.com/cosmos/cosmos-sdk/x/staking"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	vrfmodule "github.com/vexxvakan/vrf/x/vrf/module"
	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

func appModules(
	app *App,
	txConfig client.TxConfig,
	appCodec codec.Codec,
) map[string]appmodule.AppModule {
	return map[string]appmodule.AppModule{
		// Cosmos SDK modules
		genutiltypes.ModuleName: genutil.NewAppModule(
			app.AppKeepers.AccountKeeper,
			app.AppKeepers.StakingKeeper,
			app,
			txConfig,
		),
		authtypes.ModuleName: auth.NewAppModule(appCodec, app.AppKeepers.AccountKeeper, nil, nil),
		vestingtypes.ModuleName: vesting.NewAppModule(
			app.AppKeepers.AccountKeeper,
			app.AppKeepers.BankKeeper,
		),
		banktypes.ModuleName: bank.NewAppModule(
			appCodec,
			app.AppKeepers.BankKeeper,
			app.AppKeepers.AccountKeeper,
			nil,
		),
		govtypes.ModuleName: gov.NewAppModule(
			appCodec,
			app.AppKeepers.GovKeeper,
			app.AppKeepers.AccountKeeper,
			app.AppKeepers.BankKeeper,
			nil,
		),
		slashingtypes.ModuleName: slashing.NewAppModule(
			appCodec,
			app.AppKeepers.SlashingKeeper,
			app.AppKeepers.AccountKeeper,
			app.AppKeepers.BankKeeper,
			app.AppKeepers.StakingKeeper,
			nil,
			app.interfaceRegistry,
		),
		distrtypes.ModuleName: distr.NewAppModule(
			appCodec,
			app.AppKeepers.DistrKeeper,
			app.AppKeepers.AccountKeeper,
			app.AppKeepers.BankKeeper,
			app.AppKeepers.StakingKeeper,
			nil,
		),
		stakingtypes.ModuleName: staking.NewAppModule(
			appCodec,
			app.AppKeepers.StakingKeeper,
			app.AppKeepers.AccountKeeper,
			app.AppKeepers.BankKeeper,
			nil,
		),
		upgradetypes.ModuleName: upgrade.NewAppModule(
			app.AppKeepers.UpgradeKeeper,
			app.AppKeepers.AccountKeeper.AddressCodec(),
		),
		consensusparamtypes.ModuleName: consensus.NewAppModule(
			appCodec,
			app.AppKeepers.ConsensusParamsKeeper,
		),

		// chain modules
		vrftypes.ModuleName: vrfmodule.NewAppModule(appCodec, app.AppKeepers.VrfKeeper),
	}
}

// orderBeginBlockers tell the app's module manager how to set the order of
// BeginBlockers, which are run at the beginning of every block.
func orderBeginBlockers() []string {
	return []string{
		minttypes.ModuleName,
		distrtypes.ModuleName,
		slashingtypes.ModuleName,
		stakingtypes.ModuleName,
		vrftypes.ModuleName,
		authtypes.ModuleName,
		banktypes.ModuleName,
		govtypes.ModuleName,
		genutiltypes.ModuleName,
		vestingtypes.ModuleName,
		consensusparamtypes.ModuleName,
	}
}

func orderEndBlockers() []string {
	return []string{
		govtypes.ModuleName,
		stakingtypes.ModuleName,
		vrftypes.ModuleName,
		authtypes.ModuleName,
		banktypes.ModuleName,
		distrtypes.ModuleName,
		slashingtypes.ModuleName,
		minttypes.ModuleName,
		genutiltypes.ModuleName,
		upgradetypes.ModuleName,
		vestingtypes.ModuleName,
		consensusparamtypes.ModuleName,
	}
}

// NOTE: The genutils module must occur after staking so that pools are
// properly initialized with tokens from genesis accounts.
//
// NOTE: Capability module must occur first so that it can initialize any capabilities
// so that other modules that want to create or claim capabilities afterwards in InitChain
// can do so safely.
func orderInitBlockers() []string {
	return []string{
		authtypes.ModuleName,
		banktypes.ModuleName,
		vrftypes.ModuleName,
		distrtypes.ModuleName,
		stakingtypes.ModuleName,
		slashingtypes.ModuleName,
		govtypes.ModuleName,
		minttypes.ModuleName,
		genutiltypes.ModuleName,
		upgradetypes.ModuleName,
		vestingtypes.ModuleName,
		consensusparamtypes.ModuleName,
	}
}

// AppModuleBasics returns AppModuleBasics for the module BasicManager.
// used only for pre-init stuff like DefaultGenesis generation.
var AppModuleBasics = module.NewBasicManager(
	// Cosmos SDK modules
	genutil.AppModuleBasic{},
	auth.AppModuleBasic{},
	vesting.AppModuleBasic{},
	bank.AppModuleBasic{},
	gov.AppModuleBasic{},
	slashing.AppModuleBasic{},
	distr.AppModuleBasic{},
	staking.AppModuleBasic{},
	upgrade.AppModuleBasic{},
	consensus.AppModuleBasic{},

	// chain modules
	vrfmodule.AppModuleBasic{},
)
