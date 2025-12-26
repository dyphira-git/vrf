package v2

import (
	"context"
	"fmt"

	"cosmossdk.io/log"
	upgradetypes "cosmossdk.io/x/upgrade/types"

	"github.com/cosmos/cosmos-sdk/types/module"

	"github.com/vexxvakan/vrf/app/keepers"
)

func CreateV2UpgradeHandler(
	mm *module.Manager,
	cfg module.Configurator,
	_ *keepers.AppKeepers,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		logger := log.NewNopLogger()
		if lc, ok := ctx.(interface{ Logger() log.Logger }); ok {
			logger = lc.Logger()
		}
		logger = logger.With("upgrade", UpgradeName)

		// Run migrations
		logger.Info(fmt.Sprintf("v2: running migrations for: %v", vm))
		versionMap, err := mm.RunMigrations(ctx, cfg, vm)
		if err != nil {
			return nil, err
		}
		logger.Info(fmt.Sprintf("v2: post migration check: %v", versionMap))

		return versionMap, nil
	}
}
