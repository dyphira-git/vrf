package testutil

import (
	"time"

	"github.com/stretchr/testify/suite"

	storetypes "cosmossdk.io/store/types"
	txsigning "cosmossdk.io/x/tx/signing"

	"github.com/cosmos/cosmos-sdk/client/tx"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdktestutil "github.com/cosmos/cosmos-sdk/testutil"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth"
	authcodec "github.com/cosmos/cosmos-sdk/x/auth/codec"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"

	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

// VrfTestSuite provides shared setup for VRF unit tests.
type VrfTestSuite struct {
	suite.Suite

	Ctx             sdk.Context
	EncCfg          moduletestutil.TestEncodingConfig
	SignModeHandler *txsigning.HandlerMap
	AccountKeeper   authkeeper.AccountKeeper
	Authority       string

	KeyAuth *storetypes.KVStoreKey
	KeyVrf  *storetypes.KVStoreKey

	Priv cryptotypes.PrivKey
	Addr sdk.AccAddress
}

func (s *VrfTestSuite) SetupTest() {
	s.KeyAuth = storetypes.NewKVStoreKey(authtypes.StoreKey)
	s.KeyVrf = storetypes.NewKVStoreKey(vrftypes.StoreKey)

	ctx := sdktestutil.DefaultContextWithKeys(
		map[string]*storetypes.KVStoreKey{
			authtypes.StoreKey: s.KeyAuth,
			vrftypes.StoreKey:  s.KeyVrf,
		},
		map[string]*storetypes.TransientStoreKey{},
		nil,
	)
	ctx = ctx.WithChainID("test-chain").WithBlockHeight(1).WithBlockTime(time.Unix(1700000000, 0).UTC())

	encCfg := moduletestutil.MakeTestEncodingConfig(auth.AppModuleBasic{})
	vrftypes.RegisterInterfaces(encCfg.InterfaceRegistry)

	authority := authtypes.NewModuleAddress(govtypes.ModuleName).String()
	ak := authkeeper.NewAccountKeeper(
		encCfg.Codec,
		runtime.NewKVStoreService(s.KeyAuth),
		authtypes.ProtoBaseAccount,
		map[string][]string{},
		authcodec.NewBech32Codec(sdk.Bech32MainPrefix),
		sdk.Bech32MainPrefix,
		authority,
	)
	s.Require().NoError(ak.Params.Set(ctx, authtypes.DefaultParams()))

	priv, _, addr := testdata.KeyTestPubAddr()
	acc := ak.NewAccountWithAddress(ctx, addr)
	s.Require().NoError(acc.SetAccountNumber(1))
	s.Require().NoError(acc.SetPubKey(priv.PubKey()))
	ak.SetAccount(ctx, acc)

	s.Ctx = ctx
	s.EncCfg = encCfg
	s.SignModeHandler = encCfg.TxConfig.SignModeHandler()
	s.AccountKeeper = ak
	s.Authority = authority
	s.Priv = priv
	s.Addr = addr
}

func (s *VrfTestSuite) BuildSignedTx(msgs ...sdk.Msg) (authsigning.Tx, error) {
	txBuilder := s.EncCfg.TxConfig.NewTxBuilder()
	if err := txBuilder.SetMsgs(msgs...); err != nil {
		return nil, err
	}

	sig := signing.SignatureV2{
		PubKey: s.Priv.PubKey(),
		Data: &signing.SingleSignatureData{
			SignMode: signing.SignMode_SIGN_MODE_DIRECT,
		},
		Sequence: 0,
	}
	if err := txBuilder.SetSignatures(sig); err != nil {
		return nil, err
	}

	signerData := authsigning.SignerData{
		Address:       s.Addr.String(),
		ChainID:       s.Ctx.ChainID(),
		AccountNumber: 1,
		Sequence:      0,
		PubKey:        s.Priv.PubKey(),
	}

	sigV2, err := tx.SignWithPrivKey(
		s.Ctx,
		signing.SignMode_SIGN_MODE_DIRECT,
		signerData,
		txBuilder,
		s.Priv,
		s.EncCfg.TxConfig,
		0,
	)
	if err != nil {
		return nil, err
	}
	if err := txBuilder.SetSignatures(sigV2); err != nil {
		return nil, err
	}

	return txBuilder.GetTx(), nil
}
