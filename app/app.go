package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	abci "github.com/cometbft/cometbft/abci/types"
	tmjson "github.com/cometbft/cometbft/libs/json"
	tmos "github.com/cometbft/cometbft/libs/os"

	"github.com/vexxvakan/vrf/app/ante"
	"github.com/vexxvakan/vrf/app/endpoints"
	"github.com/vexxvakan/vrf/app/keepers"
	"github.com/vexxvakan/vrf/app/upgrades"
	v2 "github.com/vexxvakan/vrf/app/upgrades/v2"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/gogoproto/proto"

	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"
	reflectionv1 "cosmossdk.io/api/cosmos/reflection/v1"
	"cosmossdk.io/client/v2/autocli"
	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/log"
	"cosmossdk.io/x/tx/signing"
	upgradetypes "cosmossdk.io/x/upgrade/types"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	nodeservice "github.com/cosmos/cosmos-sdk/client/grpc/node"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	runtimeservices "github.com/cosmos/cosmos-sdk/runtime/services"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/cosmos/cosmos-sdk/server/api"
	"github.com/cosmos/cosmos-sdk/server/config"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	"github.com/cosmos/cosmos-sdk/std"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/version"
	authcodec "github.com/cosmos/cosmos-sdk/x/auth/codec"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtxconfig "github.com/cosmos/cosmos-sdk/x/auth/tx/config"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/genutil"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"

	vrfabcicodec "github.com/vexxvakan/vrf/x/vrf/abci/codec"
	vrfpreblock "github.com/vexxvakan/vrf/x/vrf/abci/preblock/vrf"
	vrfproposals "github.com/vexxvakan/vrf/x/vrf/abci/proposals"
	vrfve "github.com/vexxvakan/vrf/x/vrf/abci/ve"
	vrfconfig "github.com/vexxvakan/vrf/x/vrf/config"
	vrfsidecar "github.com/vexxvakan/vrf/x/vrf/sidecar"
)

const (
	Name = "chain"
)

var (
	NodeDir = ".chaind"
	// DefaultNodeHome default home directories for this chain
	DefaultNodeHome = os.ExpandEnv("$HOME/") + NodeDir

	Upgrades = []upgrades.Upgrade{
		v2.Upgrade,
	}

	_ runtime.AppI            = (*App)(nil)
	_ servertypes.Application = (*App)(nil)
)

// App extends an ABCI application, but with most of its parameters exported.
// They are exported for convenience in creating helper functions, as object
// capabilities aren't needed for testing.
type App struct {
	*baseapp.BaseApp
	legacyAmino       *codec.LegacyAmino
	appCodec          codec.Codec
	txConfig          client.TxConfig
	interfaceRegistry types.InterfaceRegistry
	homePath          string

	// keepers
	AppKeepers keepers.AppKeepers

	// modules
	ModuleManager      *module.Manager
	BasicModuleManager module.BasicManager
	configurator       module.Configurator

	vrfClient vrfsidecar.Client
}

// New returns a reference to an initialized chain.
func New(
	logger log.Logger,
	db dbm.DB,
	traceStore io.Writer,
	loadLatest bool,
	homePath string,
	appOpts servertypes.AppOptions,
	baseAppOptions ...func(*baseapp.BaseApp),
) *App {
	interfaceRegistry, err := types.NewInterfaceRegistryWithOptions(types.InterfaceRegistryOptions{
		ProtoFiles: proto.HybridResolver,
		SigningOptions: signing.Options{
			AddressCodec: address.Bech32Codec{
				Bech32Prefix: sdk.GetConfig().GetBech32AccountAddrPrefix(),
			},
			ValidatorAddressCodec: address.Bech32Codec{
				Bech32Prefix: sdk.GetConfig().GetBech32ValidatorAddrPrefix(),
			},
		},
	})
	if err != nil {
		panic(err)
	}

	appCodec := codec.NewProtoCodec(interfaceRegistry)
	legacyAmino := codec.NewLegacyAmino()
	txConfig := authtx.NewTxConfig(appCodec, authtx.DefaultSignModes)

	std.RegisterLegacyAminoCodec(legacyAmino)
	std.RegisterInterfaces(interfaceRegistry)

	bApp := baseapp.NewBaseApp(Name, logger, db, txConfig.TxDecoder(), baseAppOptions...)
	bApp.SetCommitMultiStoreTracer(traceStore)
	bApp.SetVersion(version.Version)
	bApp.SetInterfaceRegistry(interfaceRegistry)
	bApp.SetTxEncoder(txConfig.TxEncoder())

	app := &App{
		BaseApp:           bApp,
		legacyAmino:       legacyAmino,
		appCodec:          appCodec,
		txConfig:          txConfig,
		interfaceRegistry: interfaceRegistry,
		homePath:          homePath,
	}
	app.homePath = homePath

	app.AppKeepers = keepers.NewAppKeepers(
		appCodec,
		bApp,
		legacyAmino,
		keepers.GetMaccPerms(),
		appOpts,
		sdk.DefaultBondDenom,
		app.homePath,
	)

	// nolint:gocritic
	enabledSignModes := append(authtx.DefaultSignModes, signingtypes.SignMode_SIGN_MODE_TEXTUAL)
	txConfigOpts := authtx.ConfigOptions{
		EnabledSignModes:           enabledSignModes,
		TextualCoinMetadataQueryFn: authtxconfig.NewBankKeeperCoinMetadataQueryFn(app.AppKeepers.BankKeeper),
	}
	txConfig, err = authtx.NewTxConfigWithOptions(
		appCodec,
		txConfigOpts,
	)
	if err != nil {
		panic(err)
	}
	app.txConfig = txConfig

	vrfAppCfg, err := vrfconfig.ReadConfigFromAppOpts(appOpts)
	if err != nil {
		panic(err)
	}

	app.vrfClient = vrfsidecar.NoOpClient{}
	if vrfAppCfg.Enabled {
		vrfClient, err := vrfsidecar.NewClient(logger, vrfAppCfg.VrfAddress, vrfAppCfg.ClientTimeout)
		if err != nil {
			panic(err)
		}
		if err := vrfClient.Start(context.Background()); err != nil {
			panic(err)
		}

		app.vrfClient = vrfClient
	}

	app.configurator = module.NewConfigurator(appCodec, app.MsgServiceRouter(), app.GRPCQueryRouter())
	app.ModuleManager = module.NewManager(appModules(app, txConfig, appCodec)...)
	app.BasicModuleManager = module.NewBasicManagerFromManager(
		app.ModuleManager,
		map[string]module.AppModuleBasic{
			genutiltypes.ModuleName: genutil.NewAppModuleBasic(genutiltypes.DefaultMessageValidator),
		},
	)
	app.BasicModuleManager.RegisterLegacyAminoCodec(legacyAmino)
	app.BasicModuleManager.RegisterInterfaces(interfaceRegistry)
	err = app.ModuleManager.RegisterServices(app.configurator)
	if err != nil {
		panic(err)
	}
	app.ModuleManager.SetOrderPreBlockers(
		upgradetypes.ModuleName,
		authtypes.ModuleName,
	)
	app.ModuleManager.SetOrderBeginBlockers(orderBeginBlockers()...)
	app.ModuleManager.SetOrderEndBlockers(orderEndBlockers()...)
	app.ModuleManager.SetOrderInitGenesis(orderInitBlockers()...)
	app.ModuleManager.SetOrderExportGenesis(orderInitBlockers()...)

	// setup query services
	autocliv1.RegisterQueryServer(app.GRPCQueryRouter(), runtimeservices.NewAutoCLIQueryService(app.ModuleManager.Modules))

	// setup reflection service
	reflectionSvc, err := runtimeservices.NewReflectionService()
	if err != nil {
		panic(err)
	}
	reflectionv1.RegisterReflectionServiceServer(app.GRPCQueryRouter(), reflectionSvc)

	// initialize stores
	app.MountKVStores(app.AppKeepers.GetKVStoreKeys())

	anteHandler, err := ante.NewAnteHandler(
		ante.HandlerOptions{
			AccountKeeper:    app.AppKeepers.AccountKeeper,
			BankKeeper:       app.AppKeepers.BankKeeper,
			SignModeHandler:  txConfig.SignModeHandler(),
			FeegrantKeeper:   app.AppKeepers.FeeGrantKeeper,
			SigGasConsumer:   ante.DefaultSigVerificationGasConsumer,
			SigVerifyOptions: []ante.SigVerificationDecoratorOption{
				// change below as needed.
				// ante.WithUnorderedTxGasCost(ante.DefaultUnorderedTxGasCost),
				// ante.WithMaxUnorderedTxTimeoutDuration(ante.DefaultMaxTimeoutDuration),
			},
			VrfKeeper: &app.AppKeepers.VrfKeeper,
		},
	)
	if err != nil {
		panic(err)
	}
	app.SetAnteHandler(anteHandler)

	defaultProposalHandler := baseapp.NewDefaultProposalHandler(app.Mempool(), app)
	extendedCommitCodec := vrfabcicodec.NewCompressionExtendedCommitCodec(
		vrfabcicodec.NewDefaultExtendedCommitCodec(),
		vrfabcicodec.NewZStdCompressor(),
	)
	validateVoteExtensionsFn := vrfve.NewDefaultValidateVoteExtensionsFn(app.AppKeepers.StakingKeeper)
	vrfProposalHandler := vrfproposals.NewProposalHandler(
		logger,
		defaultProposalHandler.PrepareProposalHandler(),
		defaultProposalHandler.ProcessProposalHandler(),
		&app.AppKeepers.VrfKeeper,
		app.AppKeepers.AccountKeeper,
		txConfig.SignModeHandler(),
		txConfig.TxDecoder(),
		validateVoteExtensionsFn,
		extendedCommitCodec,
	)
	app.SetPrepareProposal(vrfProposalHandler.PrepareProposalHandler())
	app.SetProcessProposal(vrfProposalHandler.ProcessProposalHandler())

	vrfVoteExtHandler := vrfve.NewHandler(
		logger,
		app.vrfClient,
		&app.AppKeepers.VrfKeeper,
		vrfAppCfg.ClientTimeout,
	)
	app.SetExtendVoteHandler(vrfVoteExtHandler.ExtendVoteHandler())
	app.SetVerifyVoteExtensionHandler(vrfVoteExtHandler.VerifyVoteExtensionHandler())

	vrfPreBlockHandler := vrfpreblock.NewPreBlockHandler(
		logger,
		&app.AppKeepers.VrfKeeper,
		app.AppKeepers.AccountKeeper,
		txConfig.SignModeHandler(),
		txConfig.TxDecoder(),
	)

	// initialize BaseApp
	app.SetInitChainer(app.InitChainer)
	app.SetPreBlocker(vrfPreBlockHandler.WrappedPreBlocker(app.ModuleManager))
	app.SetBeginBlocker(app.BeginBlocker)
	app.SetEndBlocker(app.EndBlocker)

	app.setupUpgradeHandlers()
	app.setupUpgradeStoreLoaders()

	// At startup, after all modules have been registered, check that all proto
	// annotations are correct.
	protoFiles, err := proto.MergedRegistry()
	if err != nil {
		panic(err)
	}
	err = msgservice.ValidateProtoAnnotations(protoFiles)
	if err != nil {
		// Once we switch to using protoreflect-based antehandlers, we might
		// want to panic here instead of logging a warning.
		_, printErr := fmt.Fprintln(os.Stderr, err.Error())
		if printErr != nil {
			// This error is just thrown if println fails.
			panic(printErr)
		}
	}

	if loadLatest {
		if err := app.LoadLatestVersion(); err != nil {
			tmos.Exit(err.Error())
		}
	}

	return app
}

// AutoCLIOpts returns options based upon the modules used on the chain
func (app *App) AutoCLIOpts(initClientCtx client.Context) autocli.AppOptions {
	modules := make(map[string]appmodule.AppModule)
	for _, m := range app.ModuleManager.Modules {
		if moduleWithName, ok := m.(module.HasName); ok {
			moduleName := moduleWithName.Name()
			if appModule, ok := moduleWithName.(appmodule.AppModule); ok {
				modules[moduleName] = appModule
			}
		}
	}

	return autocli.AppOptions{
		Modules:               modules,
		ModuleOptions:         runtimeservices.ExtractAutoCLIOptions(app.ModuleManager.Modules),
		AddressCodec:          authcodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()),
		ValidatorAddressCodec: authcodec.NewBech32Codec(sdk.GetConfig().GetBech32ValidatorAddrPrefix()),
		ConsensusAddressCodec: authcodec.NewBech32Codec(sdk.GetConfig().GetBech32ConsensusAddrPrefix()),
		ClientCtx:             initClientCtx,
	}
}

// DefaultGenesis returns a default genesis from the registered AppModuleBasic's.
func (app *App) DefaultGenesis() map[string]json.RawMessage {
	return app.BasicModuleManager.DefaultGenesis(app.appCodec)
}

// Name returns the name of the App
func (app *App) Name() string {
	return app.BaseApp.Name()
}

// PreBlocker application updates every pre block
func (app *App) PreBlocker(ctx sdk.Context, _ *abci.RequestFinalizeBlock) (*sdk.ResponsePreBlock, error) {
	return app.ModuleManager.PreBlock(ctx)
}

// BeginBlocker application updates every begin block
func (app *App) BeginBlocker(ctx sdk.Context) (sdk.BeginBlock, error) {
	return app.ModuleManager.BeginBlock(ctx)
}

// EndBlocker application updates every end block
func (app *App) EndBlocker(ctx sdk.Context) (sdk.EndBlock, error) {
	return app.ModuleManager.EndBlock(ctx)
}

// InitChainer application update at chain initialization
func (app *App) InitChainer(ctx sdk.Context, req *abci.RequestInitChain) (*abci.ResponseInitChain, error) {
	var genesisState GenesisState
	if err := tmjson.Unmarshal(req.AppStateBytes, &genesisState); err != nil {
		panic(err)
	}
	if err := app.AppKeepers.UpgradeKeeper.SetModuleVersionMap(ctx, app.ModuleManager.GetVersionMap()); err != nil {
		panic(err)
	}

	response, err := app.ModuleManager.InitGenesis(ctx, app.appCodec, genesisState)
	if err != nil {
		panic(err)
	}
	return response, nil
}

// LoadHeight loads a particular height
func (app *App) LoadHeight(height int64) error {
	return app.LoadVersion(height)
}

// ModuleAccountAddrs returns all the app's module account addresses.
func (*App) ModuleAccountAddrs() map[string]bool {
	modAccAddrs := make(map[string]bool)
	for acc := range keepers.GetMaccPerms() {
		modAccAddrs[authtypes.NewModuleAddress(acc).String()] = true
	}

	return modAccAddrs
}

// LegacyAmino returns the app's amino codec.
//
// NOTE: This is solely to be used for testing purposes as it may be desirable
// for modules to register their own custom testing types.
func (app *App) LegacyAmino() *codec.LegacyAmino {
	return app.legacyAmino
}

// AppCodec returns the app's app codec.
//
// NOTE: This is solely to be used for testing purposes as it may be desirable
// for modules to register their own custom testing types.
func (app *App) AppCodec() codec.Codec {
	return app.appCodec
}

// InterfaceRegistry returns app's InterfaceRegistry
func (app *App) InterfaceRegistry() types.InterfaceRegistry {
	return app.interfaceRegistry
}

// TxConfig returns the app's TxConfig
func (app *App) TxConfig() client.TxConfig {
	return app.txConfig
}

// RegisterAPIRoutes registers all application module routes with the provided
// API server.
func (app *App) RegisterAPIRoutes(apiSvr *api.Server, _ config.APIConfig) {
	clientCtx := apiSvr.ClientCtx

	// Register Scalar UI to <address>:<port>/scalar
	// Needs to be before registering the grpc-gateway routes
	// so its not registered after a '*' wildcard route is set
	// which for some reason overrides the scalar route.
	if err := endpoints.RegisterScalarUI(apiSvr); err != nil {
		panic(err)
	}

	// Register new tx routes from grpc-gateway.
	authtx.RegisterGRPCGatewayRoutes(clientCtx, apiSvr.GRPCGatewayRouter)

	// Register new comet queries routes from grpc-gateway.
	cmtservice.RegisterGRPCGatewayRoutes(clientCtx, apiSvr.GRPCGatewayRouter)

	// Register node gRPC service for grpc-gateway.
	nodeservice.RegisterGRPCGatewayRoutes(clientCtx, apiSvr.GRPCGatewayRouter)

	// Register grpc-gateway routes for all modules.
	app.BasicModuleManager.RegisterGRPCGatewayRoutes(clientCtx, apiSvr.GRPCGatewayRouter)
}

// RegisterTxService implements the Application.RegisterTxService method.
func (app *App) RegisterTxService(clientCtx client.Context) {
	authtx.RegisterTxService(app.GRPCQueryRouter(), clientCtx, app.Simulate, app.interfaceRegistry)
}

// RegisterTendermintService implements the Application.RegisterTendermintService method.
func (app *App) RegisterTendermintService(clientCtx client.Context) {
	cmtApp := server.NewCometABCIWrapper(app)
	cmtservice.RegisterTendermintService(
		clientCtx,
		app.GRPCQueryRouter(),
		app.interfaceRegistry,
		cmtApp.Query,
	)
}

func (app *App) RegisterNodeService(clientCtx client.Context, cfg config.Config) {
	nodeservice.RegisterNodeService(clientCtx, app.GRPCQueryRouter(), cfg)
}

// SimulationManager implements the SimulationApp interface
func (app *App) SimulationManager() *module.SimulationManager {
	return nil
}

// configure store loader that checks if version == upgradeHeight and applies store upgrades
func (app *App) setupUpgradeStoreLoaders() {
	upgradeInfo, err := app.AppKeepers.UpgradeKeeper.ReadUpgradeInfoFromDisk()
	if err != nil {
		panic("failed to read upgrade info from disk: " + err.Error())
	}

	if app.AppKeepers.UpgradeKeeper.IsSkipHeight(upgradeInfo.Height) {
		return
	}

	for _, upgrade := range Upgrades {
		if upgradeInfo.Name == upgrade.UpgradeName {
			storeUpgrades := upgrade.StoreUpgrades
			app.SetStoreLoader(
				upgradetypes.UpgradeStoreLoader(upgradeInfo.Height, &storeUpgrades),
			)
		}
	}
}

func (app *App) setupUpgradeHandlers() {
	for _, upgrade := range Upgrades {
		app.AppKeepers.UpgradeKeeper.SetUpgradeHandler(
			upgrade.UpgradeName,
			upgrade.CreateUpgradeHandler(
				app.ModuleManager,
				app.configurator,
				&app.AppKeepers,
			),
		)
	}
}

func (app *App) Close() error {
	var errs []error

	if app.vrfClient != nil {
		if err := app.vrfClient.Stop(); err != nil {
			errs = append(errs, err)
		}
	}

	if err := app.BaseApp.Close(); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}
