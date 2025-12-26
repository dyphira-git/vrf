package sidecar

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"cosmossdk.io/log"

	sidecarv1 "github.com/vexxvakan/vrf/api/vexxvakan/sidecar/v1"
	vrftestutil "github.com/vexxvakan/vrf/x/vrf/testutil"
)

type SidecarSuite struct {
	vrftestutil.VrfTestSuite
}

func TestSidecarSuite(t *testing.T) {
	suite.Run(t, new(SidecarSuite))
}

func (s *SidecarSuite) TestNewClient() {
	_, err := NewClient(nil, "localhost:1", time.Second)
	s.Require().ErrorIs(err, errNilLogger)

	_, err = NewClient(log.NewNopLogger(), "localhost:1", 0)
	s.Require().ErrorIs(err, errTimeoutNotPositive)

	client, err := NewClient(log.NewNopLogger(), "localhost:1", time.Second)
	s.Require().NoError(err)
	s.Require().NotNil(client)
}

func (s *SidecarSuite) TestNotStarted() {
	client, err := NewClient(log.NewNopLogger(), "localhost:1", time.Second)
	s.Require().NoError(err)

	_, err = client.Randomness(context.Background(), &sidecarv1.QueryRandomnessRequest{})
	s.Require().ErrorIs(err, errClientNotStarted)

	_, err = client.Info(context.Background(), &sidecarv1.QueryInfoRequest{})
	s.Require().ErrorIs(err, errClientNotStarted)
}
