package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	drandcommon "github.com/drand/drand/v2/common"
	drandchain "github.com/drand/drand/v2/common/chain"
	drandkey "github.com/drand/drand/v2/common/key"
	drandcrypto "github.com/drand/drand/v2/crypto"
	"github.com/drand/kyber/share"
	"github.com/drand/kyber/share/dkg"
	"github.com/drand/kyber/util/random"

	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

type nodeMeta struct {
	Name           string `json:"name"`
	DataDir        string `json:"data_dir"`
	IdentityAddr   string `json:"identity_addr"`
	PrivateAddr    string `json:"private_addr"`
	PublicAddr     string `json:"public_addr"`
	ControlAddr    string `json:"control_addr"`
	SharePubKeyB64 string `json:"share_pubkey_b64"`
}

type metadata struct {
	ChainHashB64         string     `json:"chain_hash_b64"`
	PublicKeyB64         string     `json:"public_key_b64"`
	PeriodSeconds        uint64     `json:"period_seconds"`
	GenesisUnixSec       int64      `json:"genesis_unix_sec"`
	CatchupPeriodSeconds uint64     `json:"catchup_period_seconds"`
	Threshold            uint32     `json:"threshold"`
	SafetyMarginSeconds  uint64     `json:"safety_margin_seconds"`
	Nodes                []nodeMeta `json:"nodes"`
}

func main() {
	var (
		outDir           string
		nodeCount        int
		periodStr        string
		threshold        int
		identityHostsCSV string
	)

	flag.StringVar(&outDir, "out", "", "output directory for drand fixtures")
	flag.IntVar(&nodeCount, "nodes", 2, "number of drand nodes")
	flag.StringVar(&periodStr, "period", "2s", "drand beacon period")
	flag.IntVar(&threshold, "threshold", 2, "drand threshold (must be <= nodes)")
	flag.StringVar(&identityHostsCSV, "identity-hosts", "", "comma-separated drand identity hosts (default: 127.0.0.1 for all nodes)")
	flag.Parse()

	if outDir == "" {
		exitf("missing --out")
	}
	if nodeCount < 1 {
		exitf("nodes must be >= 1")
	}
	minThreshold := dkg.MinimumT(nodeCount)
	if threshold < minThreshold || threshold > nodeCount {
		exitf("threshold must be >= %d and <= nodes", minThreshold)
	}

	period, err := time.ParseDuration(periodStr)
	if err != nil {
		exitf("invalid period: %v", err)
	}
	if period <= 0 {
		exitf("period must be > 0")
	}
	if period%time.Second != 0 {
		exitf("period must be whole seconds")
	}

	identityHosts := parseIdentityHosts(identityHostsCSV, nodeCount)

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		exitf("create output dir: %v", err)
	}

	scheme, err := drandcrypto.SchemeFromName(drandcrypto.DefaultSchemeID)
	if err != nil {
		exitf("load drand scheme: %v", err)
	}

	periodSeconds := uint64(period / time.Second)
	safetyMargin := vrftypes.DefaultParams().SafetyMarginSeconds
	if periodSeconds > safetyMargin {
		safetyMargin = periodSeconds
	}

	genesisUnix := time.Now().Add(-(time.Duration(safetyMargin)*time.Second + 2*period)).Unix()
	catchupSeconds := periodSeconds / 2
	if catchupSeconds == 0 {
		catchupSeconds = 1
	}
	catchupPeriod := time.Duration(catchupSeconds) * time.Second

	priPoly := share.NewPriPoly(scheme.KeyGroup, threshold, scheme.KeyGroup.Scalar().Pick(random.New()), random.New())
	pubPoly := priPoly.Commit(scheme.KeyGroup.Point().Base())
	shares := priPoly.Shares(nodeCount)
	_, commits := pubPoly.Info()

	nodes := make([]*drandkey.Node, 0, nodeCount)
	keyPairs := make([]*drandkey.Pair, 0, nodeCount)
	for i := 0; i < nodeCount; i++ {
		host := identityHosts[i]
		privatePort := 4444 + i
		identityAddr := fmt.Sprintf("%s:%d", host, privatePort)
		pair, err := drandkey.NewKeyPair(identityAddr, scheme)
		if err != nil {
			exitf("create keypair: %v", err)
		}

		nodes = append(nodes, &drandkey.Node{
			Identity: pair.Public,
			Index:    drandkey.Index(shares[i].I),
		})
		keyPairs = append(keyPairs, pair)
	}

	group := &drandkey.Group{
		Threshold:     threshold,
		Period:        period,
		CatchupPeriod: catchupPeriod,
		Nodes:         nodes,
		GenesisTime:   genesisUnix,
		Scheme:        scheme,
		ID:            drandcommon.DefaultBeaconID,
		PublicKey:     &drandkey.DistPublic{Coefficients: commits},
	}

	info := drandchain.NewChainInfo(group)
	infoBytes, err := json.Marshal(info)
	if err != nil {
		exitf("marshal chain info: %v", err)
	}
	publicKeyBytes, err := info.PublicKey.MarshalBinary()
	if err != nil {
		exitf("marshal public key: %v", err)
	}

	meta := metadata{
		ChainHashB64:         base64.StdEncoding.EncodeToString(info.Hash()),
		PublicKeyB64:         base64.StdEncoding.EncodeToString(publicKeyBytes),
		PeriodSeconds:        periodSeconds,
		GenesisUnixSec:       genesisUnix,
		CatchupPeriodSeconds: catchupSeconds,
		Threshold:            uint32(threshold),
		SafetyMarginSeconds:  safetyMargin,
	}

	for i := 0; i < nodeCount; i++ {
		nodeName := fmt.Sprintf("node%d", i+1)
		nodeDir := filepath.Join(outDir, nodeName)

		if err := os.RemoveAll(nodeDir); err != nil {
			exitf("reset node dir: %v", err)
		}
		if err := os.MkdirAll(nodeDir, 0o755); err != nil {
			exitf("create node dir: %v", err)
		}

		store := drandkey.NewFileStore(filepath.Join(nodeDir, drandcommon.MultiBeaconFolder), drandcommon.DefaultBeaconID)
		keyShare := &drandkey.Share{
			DistKeyShare: dkg.DistKeyShare{
				Share:   shares[i],
				Commits: commits,
			},
			Scheme: scheme,
		}

		if err := store.SaveKeyPair(keyPairs[i]); err != nil {
			exitf("save keypair: %v", err)
		}
		if err := store.SaveShare(keyShare); err != nil {
			exitf("save share: %v", err)
		}
		if err := store.SaveGroup(group); err != nil {
			exitf("save group: %v", err)
		}
		if err := os.WriteFile(filepath.Join(nodeDir, "chain-info.json"), infoBytes, 0o644); err != nil {
			exitf("write chain info: %v", err)
		}

		sharePub := scheme.KeyGroup.Point().Mul(keyShare.PrivateShare().V, nil)
		sharePubKey, err := sharePub.MarshalBinary()
		if err != nil {
			exitf("marshal share pubkey: %v", err)
		}

		privatePort := 4444 + i
		publicPort := 8081 + i
		controlPort := 8888 + i

		meta.Nodes = append(meta.Nodes, nodeMeta{
			Name:           nodeName,
			DataDir:        nodeName,
			IdentityAddr:   fmt.Sprintf("%s:%d", identityHosts[i], privatePort),
			PrivateAddr:    fmt.Sprintf("0.0.0.0:%d", privatePort),
			PublicAddr:     fmt.Sprintf("127.0.0.1:%d", publicPort),
			ControlAddr:    fmt.Sprintf("127.0.0.1:%d", controlPort),
			SharePubKeyB64: base64.StdEncoding.EncodeToString(sharePubKey),
		})
	}

	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		exitf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "metadata.json"), metaBytes, 0o644); err != nil {
		exitf("write metadata: %v", err)
	}
}

func exitf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func parseIdentityHosts(csv string, nodeCount int) []string {
	if nodeCount < 1 {
		nodeCount = 1
	}

	defaultHost := "127.0.0.1"
	if strings.TrimSpace(csv) == "" {
		hosts := make([]string, nodeCount)
		for i := range hosts {
			hosts[i] = defaultHost
		}
		return hosts
	}

	parts := strings.Split(csv, ",")
	filtered := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		filtered = append(filtered, p)
	}

	if len(filtered) == 0 {
		hosts := make([]string, nodeCount)
		for i := range hosts {
			hosts[i] = defaultHost
		}
		return hosts
	}

	if len(filtered) == 1 && nodeCount > 1 {
		hosts := make([]string, nodeCount)
		for i := range hosts {
			hosts[i] = filtered[0]
		}
		return hosts
	}

	if len(filtered) != nodeCount {
		exitf("identity-hosts must be empty, a single host, or %d hosts", nodeCount)
	}

	return filtered
}
