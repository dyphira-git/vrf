package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	drandcommon "github.com/drand/drand/v2/common"
	drandkey "github.com/drand/drand/v2/common/key"
	"github.com/drand/drand/v2/crypto"
	"go.uber.org/zap"

	"github.com/vexxvakan/vrf/sidecar/drand"
)

var (
	errDKGConfigMissingBeaconID  = errors.New("dkg beacon id is required")
	errDKGConfigMissingThreshold = errors.New("dkg threshold is required")
	errDKGConfigMissingPeriod    = errors.New("dkg period_seconds is required")
	errDKGConfigMissingGenesis   = errors.New("dkg genesis_time_unix is required")
	errDKGConfigMissingTimeout   = errors.New("dkg timeout is required")
	errDKGConfigMissingCatchup   = errors.New("dkg catchup_period_seconds is required")
	errDKGIdentityKeyMissing     = errors.New("drand identity key is missing")
)

type dkgConfig struct {
	BeaconID             string
	Threshold            uint32
	PeriodSeconds        uint32
	GenesisUnix          int64
	Timeout              time.Duration
	CatchupPeriodSeconds uint32
	JoinerAddrs          []string
	GroupSourceAddr      string
	GroupSourceToken     string
	Scheme               string
}

func buildDKGConfig(cfg cliConfig) dkgConfig {
	joiners := make([]string, 0, len(cfg.DKGJoinerAddrs))
	for _, addr := range cfg.DKGJoinerAddrs {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		joiners = append(joiners, addr)
	}

	return dkgConfig{
		BeaconID:             strings.TrimSpace(cfg.DKGBeaconID),
		Threshold:            uint32(cfg.DKGThreshold),
		PeriodSeconds:        uint32(cfg.DKGPeriodSeconds),
		GenesisUnix:          cfg.DKGGenesisUnix,
		Timeout:              cfg.DKGTimeout,
		CatchupPeriodSeconds: uint32(cfg.DKGCatchupSeconds),
		JoinerAddrs:          joiners,
		GroupSourceAddr:      strings.TrimSpace(cfg.GroupSourceAddr),
		GroupSourceToken:     strings.TrimSpace(cfg.GroupSourceToken),
		Scheme:               crypto.DefaultSchemeID,
	}
}

func (c dkgConfig) validateRequired() error {
	if strings.TrimSpace(c.BeaconID) == "" {
		return errDKGConfigMissingBeaconID
	}
	if c.Threshold == 0 {
		return errDKGConfigMissingThreshold
	}
	if c.PeriodSeconds == 0 {
		return errDKGConfigMissingPeriod
	}
	if c.GenesisUnix == 0 {
		return errDKGConfigMissingGenesis
	}
	if c.Timeout <= 0 {
		return errDKGConfigMissingTimeout
	}
	if c.CatchupPeriodSeconds == 0 {
		return errDKGConfigMissingCatchup
	}
	return nil
}

func (c dkgConfig) role() InitialDKGRole {
	hasGroupSource := strings.TrimSpace(c.GroupSourceAddr) != ""
	if len(c.JoinerAddrs) > 0 && hasGroupSource {
		return InitialDKGRole("")
	}
	if hasGroupSource {
		return InitialDKGRoleJoiner
	}
	return InitialDKGRoleLeader
}

type dkgIdentity struct {
	BeaconID    string
	PrivateAddr string
	PublicKey   []byte
	Signature   []byte
}

func loadLocalIdentity(logger *zap.Logger, drandCfg drand.Config, cfg dkgConfig) (*dkgIdentity, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	beaconID := strings.TrimSpace(cfg.BeaconID)
	if beaconID == "" {
		return nil, errDKGConfigMissingBeaconID
	}

	base := filepath.Join(strings.TrimSpace(drandCfg.DrandDataDir), drandcommon.MultiBeaconFolder)
	if strings.TrimSpace(base) == "" {
		return nil, fmt.Errorf("drand data dir is empty")
	}

	store := drandkey.NewFileStore(base, beaconID)
	pair, err := store.LoadKeyPair()
	if err != nil {
		return nil, fmt.Errorf("loading drand identity: %w", err)
	}

	if pair == nil || pair.Public == nil || pair.Public.Key == nil {
		return nil, errDKGIdentityKeyMissing
	}

	if pair.Public.Scheme == nil {
		scheme, schemeErr := crypto.SchemeFromName(cfg.Scheme)
		if schemeErr != nil {
			return nil, fmt.Errorf("loading drand scheme: %w", schemeErr)
		}
		pair.Public.Scheme = scheme
	}

	if err := pair.Public.ValidSignature(); err != nil {
		logger.Info("drand identity signature missing or invalid; re-signing", zap.Error(err))
		if signErr := pair.SelfSign(); signErr != nil {
			return nil, fmt.Errorf("self-sign drand identity: %w", signErr)
		}
		if saveErr := store.SaveKeyPair(pair); saveErr != nil {
			return nil, fmt.Errorf("saving drand identity: %w", saveErr)
		}
	}

	pubKey, err := pair.Public.Key.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("encoding drand identity public key: %w", err)
	}

	addr := strings.TrimSpace(drandCfg.DrandPrivateListen)
	if addr == "" {
		addr = strings.TrimSpace(pair.Public.Addr)
	}

	return &dkgIdentity{
		BeaconID:    beaconID,
		PrivateAddr: addr,
		PublicKey:   pubKey,
		Signature:   append([]byte(nil), pair.Public.Signature...),
	}, nil
}

func groupFilePath(drandCfg drand.Config, cfg dkgConfig) (string, error) {
	beaconID := strings.TrimSpace(cfg.BeaconID)
	if beaconID == "" {
		return "", errDKGConfigMissingBeaconID
	}
	base := filepath.Join(strings.TrimSpace(drandCfg.DrandDataDir), drandcommon.MultiBeaconFolder)
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("drand data dir is empty")
	}
	store := drandkey.NewFileStore(base, beaconID)
	path := drandkey.GroupFilePath(store)
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("unable to determine drand group file path")
	}
	return path, nil
}

func readGroupFileBytes(drandCfg drand.Config, cfg dkgConfig) ([]byte, error) {
	path, err := groupFilePath(drandCfg, cfg)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func writeGroupFileBytes(drandCfg drand.Config, cfg dkgConfig, data []byte) error {
	path, err := groupFilePath(drandCfg, cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating group dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing group file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming group file: %w", err)
	}
	return nil
}

type identityResponse struct {
	BeaconID    string `json:"beacon_id"`
	PublicKey   string `json:"public_key"`
	Signature   string `json:"signature"`
	PrivateAddr string `json:"private_addr"`
}

func identityHandler(logger *zap.Logger, drandCfg drand.Config, cfg dkgConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, err := loadLocalIdentity(logger, drandCfg, cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		resp := identityResponse{
			BeaconID:    identity.BeaconID,
			PublicKey:   base64.StdEncoding.EncodeToString(identity.PublicKey),
			Signature:   base64.StdEncoding.EncodeToString(identity.Signature),
			PrivateAddr: identity.PrivateAddr,
		}

		b, err := json.Marshal(resp)
		if err != nil {
			http.Error(w, "failed to encode identity", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}
}

func groupHandler(drandCfg drand.Config, cfg dkgConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token := strings.TrimSpace(cfg.GroupSourceToken); token != "" {
			const prefix = "Bearer "
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, prefix) || strings.TrimSpace(strings.TrimPrefix(auth, prefix)) != token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		data, err := readGroupFileBytes(drandCfg, cfg)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.Error(w, "group file not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
	}
}
