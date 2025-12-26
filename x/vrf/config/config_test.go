package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	vrftestutil "github.com/vexxvakan/vrf/x/vrf/testutil"
)

type ConfigSuite struct {
	vrftestutil.VrfTestSuite
}

func TestConfigSuite(t *testing.T) {
	suite.Run(t, new(ConfigSuite))
}

type appOptions map[string]interface{}

func (a appOptions) Get(key string) interface{} {
	return a[key]
}

func (s *ConfigSuite) TestValidateBasic() {
	cfg := NewDefaultAppConfig()
	s.Require().NoError(cfg.ValidateBasic())

	cfg.Enabled = true
	cfg.VrfAddress = ""
	s.Require().Error(cfg.ValidateBasic())

	cfg.VrfAddress = "localhost:9090"
	cfg.ClientTimeout = 0
	s.Require().Error(cfg.ValidateBasic())
}

func (s *ConfigSuite) TestReadConfig() {
	cfg, err := ReadConfigFromAppOpts(appOptions{})
	s.Require().NoError(err)
	s.Require().Equal(NewDefaultAppConfig(), cfg)

	opts := appOptions{
		flagEnabled:       true,
		flagVrfAddress:    "127.0.0.1:9999",
		flagClientTimeout: "2s",
		flagMetrics:       true,
	}
	cfg, err = ReadConfigFromAppOpts(opts)
	s.Require().NoError(err)
	s.Require().True(cfg.Enabled)
	s.Require().Equal("127.0.0.1:9999", cfg.VrfAddress)
	s.Require().Equal(2*time.Second, cfg.ClientTimeout)
	s.Require().True(cfg.MetricsEnabled)

	_, err = ReadConfigFromAppOpts(appOptions{flagEnabled: true, flagVrfAddress: struct{}{}})
	s.Require().ErrorIs(err, errVrfAddressNotString)

	_, err = ReadConfigFromAppOpts(appOptions{flagEnabled: true, flagClientTimeout: "bad"})
	s.Require().ErrorIs(err, errVrfClientTimeoutNotDuration)
}
