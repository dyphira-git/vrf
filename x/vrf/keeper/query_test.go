package keeper

import (
	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

func (s *KeeperSuite) TestQueryParams() {
	resp, err := s.QueryServer.Params(s.Ctx, &vrftypes.QueryParamsRequest{})
	s.Require().NoError(err)
	s.Require().Equal(vrftypes.DefaultParams(), resp.Params)
}

func (s *KeeperSuite) TestQueryBeacon() {
	_, err := s.QueryServer.Beacon(s.Ctx, &vrftypes.QueryBeaconRequest{})
	s.Require().ErrorIs(err, errGetBeaconWhileDisabled)

	params := vrftypes.DefaultParams()
	params.Enabled = true
	s.Require().NoError(s.Keeper.SetParams(s.Ctx, params))
	beacon := vrftypes.VrfBeacon{DrandRound: 5, Randomness: []byte("rand")}
	s.Require().NoError(s.Keeper.SetLatestBeacon(s.Ctx, beacon))

	resp, err := s.QueryServer.Beacon(s.Ctx, &vrftypes.QueryBeaconRequest{})
	s.Require().NoError(err)
	s.Require().Equal(beacon, resp.Beacon)
}

func (s *KeeperSuite) TestQueryRandomWords() {
	_, err := s.QueryServer.RandomWords(s.Ctx, nil)
	s.Require().ErrorIs(err, errNilQueryRandomWordsRequest)

	_, err = s.QueryServer.RandomWords(s.Ctx, &vrftypes.QueryRandomWordsRequest{Count: 0})
	s.Require().ErrorIs(err, errRandomWordsCountMustBePositive)

	_, err = s.QueryServer.RandomWords(s.Ctx, &vrftypes.QueryRandomWordsRequest{Count: maxRandomWords + 1})
	s.Require().ErrorIs(err, errRandomWordsCountExceedsMax)

	_, err = s.QueryServer.RandomWords(s.Ctx, &vrftypes.QueryRandomWordsRequest{Count: 1})
	s.Require().ErrorIs(err, errGetBeaconWhileDisabled)

	params := vrftypes.DefaultParams()
	params.Enabled = true
	s.Require().NoError(s.Keeper.SetParams(s.Ctx, params))
	beacon := vrftypes.VrfBeacon{DrandRound: 7, Randomness: []byte("abcdefghijklmnopqrstuvwxyz012345")}
	s.Require().NoError(s.Keeper.SetLatestBeacon(s.Ctx, beacon))

	resp, err := s.QueryServer.RandomWords(s.Ctx, &vrftypes.QueryRandomWordsRequest{Count: 2, UserSeed: []byte{0x01}})
	s.Require().NoError(err)
	s.Require().Equal(beacon.DrandRound, resp.DrandRound)
	s.Require().Equal(beacon.Randomness, resp.Seed)
	s.Require().Len(resp.Words, 2)
}
