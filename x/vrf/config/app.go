package config

import (
	"errors"
	"time"

	"github.com/spf13/cast"

	servertypes "github.com/cosmos/cosmos-sdk/server/types"
)

var (
	DefaultEnabled       = false
	DefaultVrfAddress    = "localhost:8090"
	DefaultClientTimeout = 3 * time.Second
	DefaultMetrics       = false

	errVrfAddressEmpty             = errors.New("vrf address must not be empty")
	errVrfClientTimeoutNonPositive = errors.New("vrf client timeout must be greater than 0")
	errVrfAddressNotString         = errors.New("vrf address must be a non-empty string")
	errVrfClientTimeoutNotDuration = errors.New("vrf client timeout must be a positive duration")
)

const (
	// DefaultConfigTemplate can be embedded into app.toml if desired.
	DefaultConfigTemplate = `

###############################################################################
###                                   VRF                                   ###
###############################################################################
[vrf]
# Enabled indicates whether sidecar integration is enabled.
enabled = "{{ .Vrf.Enabled }}"

# vrf_address is the address of the out-of-process VRF sidecar.
vrf_address = "{{ .Vrf.VrfAddress }}"

# client_timeout is the maximum time the VRF client will wait for responses
# from the sidecar before timing out.
client_timeout = "{{ .Vrf.ClientTimeout }}"

# metrics_enabled determines whether VRF client metrics are enabled.
metrics_enabled = "{{ .Vrf.MetricsEnabled }}"
`
)

const (
	flagEnabled       = "vrf.enabled"
	flagVrfAddress    = "vrf.vrf_address"
	flagClientTimeout = "vrf.client_timeout"
	flagMetrics       = "vrf.metrics_enabled"
)

// AppConfig contains the application-side VRF configuration loaded from
// app.toml.
type AppConfig struct {
	Enabled        bool          `mapstructure:"enabled" toml:"enabled"`
	VrfAddress     string        `mapstructure:"vrf_address" toml:"vrf_address"`
	ClientTimeout  time.Duration `mapstructure:"client_timeout" toml:"client_timeout"`
	MetricsEnabled bool          `mapstructure:"metrics_enabled" toml:"metrics_enabled"`
}

func NewDefaultAppConfig() AppConfig {
	return AppConfig{
		Enabled:        DefaultEnabled,
		VrfAddress:     DefaultVrfAddress,
		ClientTimeout:  DefaultClientTimeout,
		MetricsEnabled: DefaultMetrics,
	}
}

func (c *AppConfig) ValidateBasic() error {
	if !c.Enabled {
		return nil
	}

	if len(c.VrfAddress) == 0 {
		return errVrfAddressEmpty
	}

	if c.ClientTimeout <= 0 {
		return errVrfClientTimeoutNonPositive
	}

	return nil
}

// ReadConfigFromAppOpts reads the vrf config parameters from the AppOptions
// and returns the effective config.
func ReadConfigFromAppOpts(opts servertypes.AppOptions) (AppConfig, error) {
	var (
		cfg = NewDefaultAppConfig()
		err error
	)

	if v := opts.Get(flagEnabled); v != nil {
		if cfg.Enabled, err = cast.ToBoolE(v); err != nil {
			return cfg, err
		}
	}

	if !cfg.Enabled {
		return cfg, nil
	}

	if v := opts.Get(flagVrfAddress); v != nil {
		address, err := cast.ToStringE(v)
		if err != nil {
			return cfg, errVrfAddressNotString
		}
		if len(address) > 0 {
			cfg.VrfAddress = address
		}
	}

	if v := opts.Get(flagClientTimeout); v != nil {
		timeout, err := cast.ToDurationE(v)
		if err != nil {
			return cfg, errVrfClientTimeoutNotDuration
		}
		if timeout > 0 {
			cfg.ClientTimeout = timeout
		}
	}

	if v := opts.Get(flagMetrics); v != nil {
		if cfg.MetricsEnabled, err = cast.ToBoolE(v); err != nil {
			return cfg, err
		}
	}

	if err := cfg.ValidateBasic(); err != nil {
		return cfg, err
	}

	return cfg, nil
}
