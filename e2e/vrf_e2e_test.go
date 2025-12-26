package e2e_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	sdkmodule "github.com/cosmos/cosmos-sdk/types/module/testutil"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"

	"github.com/cosmos/interchaintest/v10"
	"github.com/cosmos/interchaintest/v10/chain/cosmos"
	"github.com/cosmos/interchaintest/v10/dockerutil"
	"github.com/cosmos/interchaintest/v10/ibc"
	ictestutil "github.com/cosmos/interchaintest/v10/testutil"

	drandcommon "github.com/drand/drand/v2/common"
	drandchain "github.com/drand/drand/v2/common/chain"
	drandkey "github.com/drand/drand/v2/common/key"
	drandcrypto "github.com/drand/drand/v2/crypto"
	"github.com/drand/kyber/share"
	"github.com/drand/kyber/share/dkg"
	"github.com/drand/kyber/util/random"

	vrfmodule "github.com/vexxvakan/vrf/x/vrf/module"
	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

const (
	chainID            = "vrf-1"
	chainName          = "vrf"
	bech32Prefix       = "vrf"
	denom              = "uchain"
	sidecarProcessName = "vrf-sidecar"
)

type dockerTestWrapper struct {
	*testing.T
	name string
}

func (d *dockerTestWrapper) Helper() {
	d.T.Helper()
}

func (d *dockerTestWrapper) Name() string {
	return d.name
}

func (d *dockerTestWrapper) Failed() bool {
	return d.T.Failed()
}

func (d *dockerTestWrapper) Cleanup(f func()) {
	d.T.Cleanup(f)
}

func (d *dockerTestWrapper) Logf(format string, args ...any) {
	d.T.Logf(format, args...)
}

func TestVRFEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping VRF e2e in short mode")
	}

	ctx := context.Background()

	cosmos.SetSDKConfig(bech32Prefix)

	testName := fmt.Sprintf("%s-%s", t.Name(), dockerutil.RandLowerCaseLetterString(4))
	dockerT := &dockerTestWrapper{T: t, name: testName}
	chainHost := chainHostName(chainID, testName, 0)
	sidecarHost := sidecarHostName(chainID, testName, sidecarProcessName, 0)

	chainImage := dockerImageFromEnv("VRF_CHAIN_IMAGE", "VRF_CHAIN_TAG", "vrf-chain", "local")
	sidecarImage := dockerImageFromEnv("VRF_SIDECAR_IMAGE", "VRF_SIDECAR_TAG", "vrf-sidecar", "local")

	appTomlOverrides := ictestutil.Toml{
		"grpc": ictestutil.Toml{
			"address": "0.0.0.0:9090",
		},
		"vrf": ictestutil.Toml{
			"enabled":         true,
			"vrf_address":     fmt.Sprintf("%s:8090", sidecarHost),
			"client_timeout":  "3s",
			"metrics_enabled": false,
		},
	}

	genesisOverrides := []cosmos.GenesisKV{
		cosmos.NewGenesisKV("consensus.params.abci.vote_extensions_enable_height", "2"),
		cosmos.NewGenesisKV("app_state.gov.params.voting_period", "30s"),
		cosmos.NewGenesisKV("app_state.gov.params.max_deposit_period", "30s"),
		cosmos.NewGenesisKV("app_state.gov.params.min_deposit.0.denom", denom),
		cosmos.NewGenesisKV("app_state.gov.params.min_deposit.0.amount", "1"),
	}

	configOverrides := map[string]any{
		"config/app.toml": appTomlOverrides,
	}

	sidecarHome := path.Join("/var/cosmos-chain", chainName)
	drandDataDir := path.Join(sidecarHome, "drand")

	sidecarStartCmd := []string{
		"/bin/sidecar",
		"start",
		"--listen-addr=0.0.0.0:8090",
	}

	chainCfg := ibc.ChainConfig{
		Type:                "cosmos",
		Name:                chainName,
		ChainID:             chainID,
		Images:              []ibc.DockerImage{chainImage},
		Bin:                 "chaind",
		Bech32Prefix:        bech32Prefix,
		Denom:               denom,
		CoinType:            "118",
		GasPrices:           "0" + denom,
		GasAdjustment:       1.3,
		Gas:                 "auto",
		TrustingPeriod:      "112h",
		EncodingConfig:      vrfEncodingConfig(),
		ModifyGenesis:       cosmos.ModifyGenesis(genesisOverrides),
		ConfigFileOverrides: configOverrides,
		SidecarConfigs: []ibc.SidecarConfig{
			{
				ProcessName:      sidecarProcessName,
				Image:            sidecarImage,
				HomeDir:          sidecarHome,
				StartCmd:         sidecarStartCmd,
				Env:              []string{fmt.Sprintf("CHAIN_HOME=%s", sidecarHome)},
				PreStart:         false,
				ValidatorProcess: true,
			},
		},
	}

	numVals := 1
	numFull := 0

	chainSpec := &interchaintest.ChainSpec{
		Name:          chainName,
		ChainName:     chainName,
		Version:       chainImage.Version,
		ChainConfig:   chainCfg,
		NumValidators: &numVals,
		NumFullNodes:  &numFull,
	}

	logger := zaptest.NewLogger(t)
	cf := interchaintest.NewBuiltinChainFactory(logger, []*interchaintest.ChainSpec{chainSpec})
	chains, err := cf.Chains(testName)
	require.NoError(t, err)

	chain := chains[0].(*cosmos.CosmosChain)
	client, network := interchaintest.DockerSetup(dockerT)

	require.NoError(t, chain.Initialize(ctx, testName, client, network))
	t.Cleanup(func() {
		_ = chain.StopAllSidecars(ctx)
		_ = chain.StopAllNodes(ctx)
	})

	fixture := generateDrandFixture(t, 2*time.Second)
	validator := chain.Validators[0]
	require.Len(t, validator.Sidecars, 1)
	require.NoError(t, copyDirToSidecar(ctx, validator.Sidecars[0], fixture.DataDir, "drand"))

	require.NoError(t, chain.Start(testName, ctx))
	require.NoError(t, ictestutil.WaitForBlocks(ctx, 2, chain))
	require.NoError(t, copyChainConfigToSidecar(ctx, validator, validator.Sidecars[0], chainHost))
	require.NoError(t, initVrfTomlWithSidecar(ctx, validator.Sidecars[0], sidecarHome, drandDataDir, fixture))
	require.NoError(t, chain.StartAllValSidecars(ctx))

	node := chain.GetNode()
	valAddr, err := node.AccountKeyBech32(ctx, "validator")
	require.NoError(t, err)

	govAddr, err := chain.AuthQueryModuleAddress(ctx, "gov")
	require.NoError(t, err)

	committeeMsg := &vrftypes.MsgAddVrfCommitteeMember{
		Authority: govAddr,
		Address:   valAddr,
		Label:     "validator",
	}
	committeeProp, err := chain.BuildProposal(
		[]cosmos.ProtoMessage{committeeMsg},
		"Add VRF committee member",
		"Add VRF committee member",
		"",
		"1"+denom,
		"",
		false,
	)
	require.NoError(t, err)
	submitAndPassGovProposal(ctx, t, chain, node, "validator", committeeProp, 1)

	_, err = node.ExecTx(
		ctx,
		"validator",
		"vrf",
		"initial-dkg",
		"--chain-hash", base64.StdEncoding.EncodeToString(fixture.ChainHash),
		"--public-key", base64.StdEncoding.EncodeToString(fixture.PublicKey),
		"--period-seconds", strconv.FormatUint(fixture.PeriodSeconds, 10),
		"--genesis-unix-sec", strconv.FormatInt(fixture.GenesisUnixSec, 10),
	)
	require.NoError(t, err)

	_, err = node.ExecTx(
		ctx,
		"validator",
		"vrf",
		"register-identity",
		"--drand-bls-public-key", base64.StdEncoding.EncodeToString(fixture.SharePubKey),
	)
	require.NoError(t, err)

	queryClient := vrftypes.NewQueryClient(node.GrpcConn)
	paramsResp, err := queryClient.Params(ctx, &vrftypes.QueryParamsRequest{})
	require.NoError(t, err)

	params := paramsResp.Params
	params.Enabled = true

	updateMsg := &vrftypes.MsgUpdateParams{
		Authority: govAddr,
		Params:    params,
	}
	updateProp, err := chain.BuildProposal(
		[]cosmos.ProtoMessage{updateMsg},
		"Enable VRF",
		"Enable VRF",
		"",
		"1"+denom,
		"",
		false,
	)
	require.NoError(t, err)
	submitAndPassGovProposal(ctx, t, chain, node, "validator", updateProp, 2)

	first := waitForBeacon(ctx, t, queryClient, 10*time.Second)
	second := waitForBeaconRound(ctx, t, queryClient, first.DrandRound, 10*time.Second)

	require.NotEqual(t, first.DrandRound, second.DrandRound)
	require.NotEqual(t, first.Randomness, second.Randomness)
}

type drandFixture struct {
	DataDir              string
	ChainHash            []byte
	PublicKey            []byte
	PeriodSeconds        uint64
	GenesisUnixSec       int64
	CatchupPeriodSeconds uint64
	Threshold            uint32
	SharePubKey          []byte
}

func generateDrandFixture(t *testing.T, period time.Duration) drandFixture {
	t.Helper()

	scheme, err := drandcrypto.SchemeFromName(drandcrypto.DefaultSchemeID)
	require.NoError(t, err)

	safetyMargin := time.Duration(vrftypes.DefaultParams().SafetyMarginSeconds) * time.Second
	genesisUnix := time.Now().Add(-(safetyMargin + 2*period)).Unix()

	pair, err := drandkey.NewKeyPair("127.0.0.1:4444", scheme)
	require.NoError(t, err)

	priPoly := share.NewPriPoly(scheme.KeyGroup, 1, scheme.KeyGroup.Scalar().Pick(random.New()), random.New())
	pubPoly := priPoly.Commit(scheme.KeyGroup.Point().Base())
	shares := priPoly.Shares(1)
	_, commits := pubPoly.Info()

	keyShare := &drandkey.Share{
		DistKeyShare: dkg.DistKeyShare{
			Share:   shares[0],
			Commits: commits,
		},
		Scheme: scheme,
	}

	group := &drandkey.Group{
		Threshold:     1,
		Period:        period,
		CatchupPeriod: period / 2,
		Nodes: []*drandkey.Node{
			{
				Identity: pair.Public,
				Index:    drandkey.Index(shares[0].I),
			},
		},
		GenesisTime: genesisUnix,
		Scheme:      scheme,
		ID:          drandcommon.DefaultBeaconID,
		PublicKey:   &drandkey.DistPublic{Coefficients: commits},
	}

	tmpDir := t.TempDir()
	drandDir := filepath.Join(tmpDir, "drand")
	require.NoError(t, os.MkdirAll(drandDir, 0o755))
	store := drandkey.NewFileStore(filepath.Join(drandDir, drandcommon.MultiBeaconFolder), drandcommon.DefaultBeaconID)
	require.NoError(t, store.SaveKeyPair(pair))
	require.NoError(t, store.SaveShare(keyShare))
	require.NoError(t, store.SaveGroup(group))

	info := drandchain.NewChainInfo(group)
	infoBytes, err := json.Marshal(info)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(drandDir, "chain-info.json"), infoBytes, 0o644))
	publicKey, err := info.PublicKey.MarshalBinary()
	require.NoError(t, err)

	sharePub := scheme.KeyGroup.Point().Mul(keyShare.PrivateShare().V, nil)
	sharePubKey, err := sharePub.MarshalBinary()
	require.NoError(t, err)

	return drandFixture{
		DataDir:              drandDir,
		ChainHash:            info.Hash(),
		PublicKey:            publicKey,
		PeriodSeconds:        uint64(period.Seconds()),
		GenesisUnixSec:       genesisUnix,
		CatchupPeriodSeconds: uint64(group.CatchupPeriod.Seconds()),
		Threshold:            uint32(group.Threshold),
		SharePubKey:          sharePubKey,
	}
}

func copyDirToSidecar(ctx context.Context, sidecar *cosmos.SidecarProcess, srcDir, dstRel string) error {
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		dstPath := filepath.ToSlash(filepath.Join(dstRel, rel))
		return sidecar.CopyFile(ctx, path, dstPath)
	})
}

func copyChainConfigToSidecar(ctx context.Context, node *cosmos.ChainNode, sidecar *cosmos.SidecarProcess, chainHost string) error {
	files := []string{
		"config/app.toml",
		"config/config.toml",
		"config/client.toml",
	}

	for _, rel := range files {
		content, err := node.ReadFile(ctx, rel)
		if err != nil {
			return err
		}

		if rel == "config/client.toml" {
			content, err = rewriteClientToml(content, chainHost)
			if err != nil {
				return err
			}
		}

		if err := sidecar.WriteFile(ctx, content, rel); err != nil {
			return err
		}
	}

	return nil
}

func rewriteClientToml(content []byte, chainHost string) ([]byte, error) {
	var cfg map[string]any
	if _, err := toml.Decode(string(content), &cfg); err != nil {
		return nil, err
	}

	cfg["node"] = fmt.Sprintf("http://%s:26657", chainHost)
	cfg["grpc-addr"] = fmt.Sprintf("%s:9090", chainHost)

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func initVrfTomlWithSidecar(ctx context.Context, sidecar *cosmos.SidecarProcess, chainHome, drandDataDir string, fixture drandFixture) error {
	if err := runSidecarCommand(ctx, sidecar, []string{"/bin/sidecar", "init", chainHome}); err != nil {
		return err
	}

	vrfPath := path.Join(chainHome, "config", "vrf.toml")
	if err := runSidecarCommand(ctx, sidecar, []string{"/bin/sidecar", "config", "--file", vrfPath, "set", "allow_public_bind", "true"}); err != nil {
		return err
	}
	if err := runSidecarCommand(ctx, sidecar, []string{"/bin/sidecar", "config", "--file", vrfPath, "set", "data_dir", drandDataDir}); err != nil {
		return err
	}
	if err := runSidecarCommand(ctx, sidecar, []string{"/bin/sidecar", "config", "--file", vrfPath, "set", "dkg_threshold", strconv.FormatUint(uint64(fixture.Threshold), 10)}); err != nil {
		return err
	}
	if err := runSidecarCommand(ctx, sidecar, []string{"/bin/sidecar", "config", "--file", vrfPath, "set", "dkg_period_seconds", strconv.FormatUint(fixture.PeriodSeconds, 10)}); err != nil {
		return err
	}
	if err := runSidecarCommand(ctx, sidecar, []string{"/bin/sidecar", "config", "--file", vrfPath, "set", "dkg_genesis_time_unix", strconv.FormatInt(fixture.GenesisUnixSec, 10)}); err != nil {
		return err
	}
	if err := runSidecarCommand(ctx, sidecar, []string{"/bin/sidecar", "config", "--file", vrfPath, "set", "dkg_timeout", "1h"}); err != nil {
		return err
	}
	if err := runSidecarCommand(ctx, sidecar, []string{"/bin/sidecar", "config", "--file", vrfPath, "set", "dkg_catchup_period_seconds", strconv.FormatUint(fixture.CatchupPeriodSeconds, 10)}); err != nil {
		return err
	}

	return nil
}

func runSidecarCommand(ctx context.Context, sidecar *cosmos.SidecarProcess, cmd []string) error {
	stdout, stderr, err := sidecar.Exec(ctx, cmd, nil)
	if err != nil {
		return fmt.Errorf("sidecar exec failed: %w (stderr=%s stdout=%s)", err, string(stderr), string(stdout))
	}
	return nil
}

func submitAndPassGovProposal(
	ctx context.Context,
	t *testing.T,
	chain *cosmos.CosmosChain,
	node *cosmos.ChainNode,
	keyName string,
	prop cosmos.TxProposalv1,
	proposalID uint64,
) {
	t.Helper()

	_, err := node.GovSubmitProposal(ctx, keyName, prop)
	require.NoError(t, err)

	var status govv1.ProposalStatus
	require.Eventually(t, func() bool {
		proposal, err := chain.GovQueryProposalV1(ctx, proposalID)
		if err != nil {
			return false
		}
		status = proposal.Status
		return status == govv1.ProposalStatus_PROPOSAL_STATUS_VOTING_PERIOD ||
			status == govv1.ProposalStatus_PROPOSAL_STATUS_PASSED
	}, 30*time.Second, 2*time.Second)

	if status != govv1.ProposalStatus_PROPOSAL_STATUS_PASSED {
		require.NoError(t, chain.VoteOnProposalAllValidators(ctx, proposalID, "yes"))
	}

	require.Eventually(t, func() bool {
		proposal, err := chain.GovQueryProposalV1(ctx, proposalID)
		if err != nil {
			return false
		}
		return proposal.Status == govv1.ProposalStatus_PROPOSAL_STATUS_PASSED
	}, 60*time.Second, 2*time.Second)
}

func waitForBeacon(ctx context.Context, t *testing.T, qc vrftypes.QueryClient, timeout time.Duration) vrftypes.VrfBeacon {
	t.Helper()

	var beacon vrftypes.VrfBeacon
	require.Eventually(t, func() bool {
		resp, err := qc.Beacon(ctx, &vrftypes.QueryBeaconRequest{})
		if err != nil {
			return false
		}
		beacon = resp.Beacon
		return beacon.DrandRound > 0 && len(beacon.Randomness) > 0
	}, timeout, 2*time.Second)

	return beacon
}

func waitForBeaconRound(
	ctx context.Context,
	t *testing.T,
	qc vrftypes.QueryClient,
	startRound uint64,
	timeout time.Duration,
) vrftypes.VrfBeacon {
	t.Helper()

	var beacon vrftypes.VrfBeacon
	require.Eventually(t, func() bool {
		resp, err := qc.Beacon(ctx, &vrftypes.QueryBeaconRequest{})
		if err != nil {
			return false
		}
		beacon = resp.Beacon
		return beacon.DrandRound > startRound
	}, timeout, 2*time.Second)

	return beacon
}

func vrfEncodingConfig() *sdkmodule.TestEncodingConfig {
	cfg := cosmos.DefaultEncoding()
	vrfmodule.AppModuleBasic{}.RegisterInterfaces(cfg.InterfaceRegistry)
	vrftypes.RegisterLegacyAminoCodec(cfg.Amino)
	return &cfg
}

func dockerImageFromEnv(repoKey, tagKey, defaultRepo, defaultTag string) ibc.DockerImage {
	repo := envOrDefault(repoKey, defaultRepo)
	tag := envOrDefault(tagKey, defaultTag)
	return ibc.DockerImage{
		Repository: repo,
		Version:    tag,
		UIDGID:     dockerutil.GetRootUserString(),
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func chainHostName(chainID, testName string, index int) string {
	name := fmt.Sprintf("%s-val-%d-%s", chainID, index, dockerutil.SanitizeContainerName(testName))
	return dockerutil.CondenseHostName(name)
}

func sidecarHostName(chainID, testName, processName string, index int) string {
	name := fmt.Sprintf("%s-%s-val-%d-%s", chainID, processName, index, dockerutil.SanitizeContainerName(testName))
	return dockerutil.CondenseHostName(name)
}
