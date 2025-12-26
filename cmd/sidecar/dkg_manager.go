package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	drandcommon "github.com/drand/drand/v2/common"
	drandchain "github.com/drand/drand/v2/common/chain"
	drandkey "github.com/drand/drand/v2/common/key"
	dkgrpc "github.com/drand/drand/v2/protobuf/dkg"
	drandgrpc "github.com/drand/drand/v2/protobuf/drand"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vexxvakan/vrf/sidecar/chainws"
	"github.com/vexxvakan/vrf/sidecar/drand"
)

var (
	errDKGAlreadyRunning   = errors.New("dkg already in progress")
	errDKGRoleConflict     = errors.New("dkg role is ambiguous (both dkg_joiner_addrs and group_source_addr are set)")
	errDKGInvalidGroup     = errors.New("invalid group file for dkg")
	errDKGInvalidChainInfo = errors.New("invalid chain info for dkg")
	errDKGSchemeMismatch   = errors.New("drand scheme mismatch")
)

type dkgManager struct {
	cfg      dkgConfig
	drandCfg drand.Config
	ctl      *drandController
	logger   *zap.Logger

	mu      sync.Mutex
	running bool
}

func newDKGManager(cfg dkgConfig, drandCfg drand.Config, ctl *drandController, logger *zap.Logger) *dkgManager {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &dkgManager{
		cfg:      cfg,
		drandCfg: drandCfg,
		ctl:      ctl,
		logger:   logger.With(zap.String("component", "sidecar-dkg")),
	}
}

func (m *dkgManager) withLock(fn func() error) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return errDKGAlreadyRunning
	}
	m.running = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()

	return fn()
}

func (m *dkgManager) RunInitial(ctx context.Context, info chainws.InitialDKGEvent) error {
	if m == nil {
		return errors.New("nil dkg manager")
	}

	return m.withLock(func() error {
		if err := m.cfg.validateRequired(); err != nil {
			return err
		}
		if skip, err := m.skipInitialIfGroupMatches(info); err != nil {
			return err
		} else if skip {
			m.logger.Info(
				"initial DKG already satisfied by local group; skipping",
				zap.String("beacon_id", m.cfg.BeaconID),
			)
			return nil
		}
		runCtx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
		defer cancel()
		ctx = runCtx
		role := m.cfg.role()
		if role == "" {
			return errDKGRoleConflict
		}

		if info.PeriodSeconds != 0 && uint32(info.PeriodSeconds) != m.cfg.PeriodSeconds {
			return fmt.Errorf("initial DKG period mismatch: event=%d config=%d", info.PeriodSeconds, m.cfg.PeriodSeconds)
		}
		if info.GenesisUnixSec != 0 && info.GenesisUnixSec != m.cfg.GenesisUnix {
			return fmt.Errorf("initial DKG genesis mismatch: event=%d config=%d", info.GenesisUnixSec, m.cfg.GenesisUnix)
		}

		if m.ctl != nil {
			if err := m.ctl.Start(ctx); err != nil {
				return err
			}
		}

		conn, dkgClient, controlClient, err := m.dialControl(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()

		complete, err := dkgAlreadyComplete(ctx, dkgClient, m.cfg.BeaconID)
		if err != nil {
			return err
		}
		if complete {
			m.logger.Info("initial DKG already complete; skipping")
			return nil
		}

		switch role {
		case InitialDKGRoleLeader:
			return m.runInitialLeader(ctx, dkgClient, controlClient)
		case InitialDKGRoleJoiner:
			return m.runInitialJoiner(ctx, dkgClient, controlClient)
		default:
			return errDKGRoleConflict
		}
	})
}

func (m *dkgManager) RunReshare(ctx context.Context, evt chainws.ReshareScheduledEvent) error {
	if m == nil {
		return errors.New("nil dkg manager")
	}

	return m.withLock(func() error {
		if err := m.cfg.validateRequired(); err != nil {
			return err
		}
		runCtx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
		defer cancel()
		ctx = runCtx
		role := m.cfg.role()
		if role == "" {
			return errDKGRoleConflict
		}

		if m.ctl != nil {
			if err := m.ctl.Start(ctx); err != nil {
				return err
			}
		}

		conn, dkgClient, controlClient, err := m.dialControl(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()

		switch role {
		case InitialDKGRoleLeader:
			return m.runReshareLeader(ctx, dkgClient, controlClient, evt)
		case InitialDKGRoleJoiner:
			return m.runReshareJoiner(ctx, dkgClient, controlClient, evt)
		default:
			return errDKGRoleConflict
		}
	})
}

func (m *dkgManager) skipInitialIfGroupMatches(info chainws.InitialDKGEvent) (bool, error) {
	_, group, err := loadLocalGroup(m.drandCfg, m.cfg)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if group == nil {
		return false, nil
	}
	if err := validateGroupAgainstConfig(group, m.cfg); err != nil {
		return false, err
	}

	chainHash := info.ChainHash
	if len(chainHash) == 0 {
		return true, nil
	}
	chainInfo := drandchain.NewChainInfo(group)
	if !bytes.Equal(chainInfo.Hash(), chainHash) {
		return false, fmt.Errorf("existing drand group chain hash mismatch: got %x expected %x", chainInfo.Hash(), chainHash)
	}

	return true, nil
}

func (m *dkgManager) runInitialLeader(ctx context.Context, dkgClient dkgrpc.DKGControlClient, controlClient drandgrpc.ControlClient) error {
	self, err := loadLocalIdentity(m.logger, m.drandCfg, m.cfg)
	if err != nil {
		return err
	}

	participants := []*dkgrpc.Participant{
		{
			Address:   self.PrivateAddr,
			Key:       self.PublicKey,
			Signature: self.Signature,
		},
	}

	joiners, err := fetchJoinerIdentities(ctx, m.cfg)
	if err != nil {
		return err
	}
	for _, ident := range joiners {
		participants = appendParticipant(participants, ident)
	}
	if int(m.cfg.Threshold) > len(participants) {
		return fmt.Errorf("dkg threshold exceeds participants: threshold=%d participants=%d", m.cfg.Threshold, len(participants))
	}

	timeout := timestamppb.New(time.Now().Add(m.cfg.Timeout))
	genesis := timestamppb.New(time.Unix(m.cfg.GenesisUnix, 0))

	cmd := &dkgrpc.DKGCommand{
		Metadata: &dkgrpc.CommandMetadata{BeaconID: m.cfg.BeaconID},
		Command: &dkgrpc.DKGCommand_Initial{Initial: &dkgrpc.FirstProposalOptions{
			Timeout:              timeout,
			Threshold:            m.cfg.Threshold,
			PeriodSeconds:        m.cfg.PeriodSeconds,
			Scheme:               m.cfg.Scheme,
			CatchupPeriodSeconds: m.cfg.CatchupPeriodSeconds,
			GenesisTime:          genesis,
			Joining:              participants,
		}},
	}

	if _, err := dkgClient.Command(ctx, cmd); err != nil {
		return fmt.Errorf("initial DKG proposal failed: %w", err)
	}

	if err := sendAccept(ctx, dkgClient, m.cfg.BeaconID); err != nil {
		return err
	}
	if err := sendExecute(ctx, dkgClient, m.cfg.BeaconID); err != nil {
		return err
	}

	if err := waitForDKGComplete(ctx, dkgClient, m.cfg.BeaconID, m.cfg.Timeout); err != nil {
		return err
	}

	return m.persistResults(ctx, controlClient)
}

func (m *dkgManager) runInitialJoiner(ctx context.Context, dkgClient dkgrpc.DKGControlClient, controlClient drandgrpc.ControlClient) error {
	groupBytes, group, err := fetchGroupFileWithRetry(ctx, m.cfg, m.cfg.Timeout)
	if err != nil {
		return err
	}

	if err := validateGroupAgainstConfig(group, m.cfg); err != nil {
		_ = sendAbort(ctx, dkgClient, m.cfg.BeaconID)
		return err
	}

	if err := writeGroupFileBytes(m.drandCfg, m.cfg, groupBytes); err != nil {
		_ = sendAbort(ctx, dkgClient, m.cfg.BeaconID)
		return err
	}

	if _, err := dkgClient.Command(ctx, &dkgrpc.DKGCommand{
		Metadata: &dkgrpc.CommandMetadata{BeaconID: m.cfg.BeaconID},
		Command: &dkgrpc.DKGCommand_Join{Join: &dkgrpc.JoinOptions{
			GroupFile: groupBytes,
		}},
	}); err != nil {
		return fmt.Errorf("dkg join failed: %w", err)
	}

	if err := sendAccept(ctx, dkgClient, m.cfg.BeaconID); err != nil {
		return err
	}
	if err := sendExecute(ctx, dkgClient, m.cfg.BeaconID); err != nil {
		return err
	}

	if err := waitForDKGComplete(ctx, dkgClient, m.cfg.BeaconID, m.cfg.Timeout); err != nil {
		return err
	}

	return m.persistResults(ctx, controlClient)
}

func (m *dkgManager) runReshareLeader(ctx context.Context, dkgClient dkgrpc.DKGControlClient, controlClient drandgrpc.ControlClient, _ chainws.ReshareScheduledEvent) error {
	_, group, err := loadLocalGroup(m.drandCfg, m.cfg)
	if err != nil {
		return err
	}
	if err := validateGroupAgainstConfig(group, m.cfg); err != nil {
		return err
	}

	remaining := participantsFromGroup(group)
	joiners, err := fetchJoinerIdentities(ctx, m.cfg)
	if err != nil {
		return err
	}
	joiners = filterJoiners(remaining, joiners)
	if int(m.cfg.Threshold) > len(remaining)+len(joiners) {
		return fmt.Errorf("reshare threshold exceeds participants: threshold=%d participants=%d", m.cfg.Threshold, len(remaining)+len(joiners))
	}

	timeout := timestamppb.New(time.Now().Add(m.cfg.Timeout))

	cmd := &dkgrpc.DKGCommand{
		Metadata: &dkgrpc.CommandMetadata{BeaconID: m.cfg.BeaconID},
		Command: &dkgrpc.DKGCommand_Resharing{Resharing: &dkgrpc.ProposalOptions{
			Timeout:              timeout,
			Threshold:            m.cfg.Threshold,
			CatchupPeriodSeconds: m.cfg.CatchupPeriodSeconds,
			Joining:              []*dkgrpc.Participant{},
			Leaving:              []*dkgrpc.Participant{},
			Remaining:            remaining,
		}},
	}

	if len(joiners) > 0 {
		cmd.GetResharing().Joining = participantsFromIdentities(joiners)
	}

	if _, err := dkgClient.Command(ctx, cmd); err != nil {
		return fmt.Errorf("reshare proposal failed: %w", err)
	}

	if err := sendAccept(ctx, dkgClient, m.cfg.BeaconID); err != nil {
		return err
	}
	if err := sendExecute(ctx, dkgClient, m.cfg.BeaconID); err != nil {
		return err
	}

	if err := waitForDKGComplete(ctx, dkgClient, m.cfg.BeaconID, m.cfg.Timeout); err != nil {
		return err
	}

	return m.persistResults(ctx, controlClient)
}

func (m *dkgManager) runReshareJoiner(ctx context.Context, dkgClient dkgrpc.DKGControlClient, controlClient drandgrpc.ControlClient, _ chainws.ReshareScheduledEvent) error {
	groupBytes, group, err := fetchGroupFileWithRetry(ctx, m.cfg, m.cfg.Timeout)
	if err != nil {
		return err
	}

	if err := validateGroupAgainstConfig(group, m.cfg); err != nil {
		_ = sendAbort(ctx, dkgClient, m.cfg.BeaconID)
		return err
	}

	if err := writeGroupFileBytes(m.drandCfg, m.cfg, groupBytes); err != nil {
		_ = sendAbort(ctx, dkgClient, m.cfg.BeaconID)
		return err
	}

	if _, err := dkgClient.Command(ctx, &dkgrpc.DKGCommand{
		Metadata: &dkgrpc.CommandMetadata{BeaconID: m.cfg.BeaconID},
		Command:  &dkgrpc.DKGCommand_Join{Join: &dkgrpc.JoinOptions{GroupFile: groupBytes}},
	}); err != nil {
		return fmt.Errorf("reshare join failed: %w", err)
	}

	if err := sendAccept(ctx, dkgClient, m.cfg.BeaconID); err != nil {
		return err
	}
	if err := sendExecute(ctx, dkgClient, m.cfg.BeaconID); err != nil {
		return err
	}

	if err := waitForDKGComplete(ctx, dkgClient, m.cfg.BeaconID, m.cfg.Timeout); err != nil {
		return err
	}

	return m.persistResults(ctx, controlClient)
}

func (m *dkgManager) persistResults(ctx context.Context, controlClient drandgrpc.ControlClient) error {
	groupPkt, err := controlClient.GroupFile(ctx, &drandgrpc.GroupRequest{
		Metadata: &drandgrpc.Metadata{BeaconID: m.cfg.BeaconID},
	})
	if err != nil {
		return fmt.Errorf("fetching group file: %w", err)
	}
	group, err := drandkey.GroupFromProto(groupPkt, nil)
	if err != nil {
		return fmt.Errorf("decoding group file: %w", err)
	}
	if err := validateGroupAgainstConfig(group, m.cfg); err != nil {
		return err
	}

	groupBytes, err := encodeGroupToml(group)
	if err != nil {
		return err
	}
	if err := writeGroupFileBytes(m.drandCfg, m.cfg, groupBytes); err != nil {
		return err
	}

	chainPkt, err := controlClient.ChainInfo(ctx, &drandgrpc.ChainInfoRequest{
		Metadata: &drandgrpc.Metadata{BeaconID: m.cfg.BeaconID},
	})
	if err != nil {
		return fmt.Errorf("fetching chain info: %w", err)
	}
	chainInfo, err := drandchain.InfoFromProto(chainPkt)
	if err != nil {
		return fmt.Errorf("decoding chain info: %w", err)
	}
	if err := validateChainInfoAgainstConfig(chainInfo, m.cfg); err != nil {
		return err
	}

	if err := writeChainInfoFile(m.drandCfg, chainInfo); err != nil {
		return err
	}

	return nil
}

func encodeGroupToml(group *drandkey.Group) ([]byte, error) {
	if group == nil {
		return nil, errDKGInvalidGroup
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(group.TOML()); err != nil {
		return nil, fmt.Errorf("encoding group TOML: %w", err)
	}
	return buf.Bytes(), nil
}

func writeChainInfoFile(cfg drand.Config, info *drandchain.Info) error {
	if info == nil {
		return errDKGInvalidChainInfo
	}
	path := filepath.Join(strings.TrimSpace(cfg.DrandDataDir), "chain-info.json")
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("drand data dir is empty")
	}
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("encoding chain info: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing chain info: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming chain info: %w", err)
	}
	return nil
}

func validateGroupAgainstConfig(group *drandkey.Group, cfg dkgConfig) error {
	if group == nil {
		return errDKGInvalidGroup
	}
	schemeName := ""
	if group.Scheme != nil {
		schemeName = group.Scheme.Name
	}
	if schemeName != cfg.Scheme {
		return fmt.Errorf("%w: group scheme=%q expected=%q", errDKGSchemeMismatch, schemeName, cfg.Scheme)
	}
	if !drandcommon.CompareBeaconIDs(group.ID, cfg.BeaconID) {
		return fmt.Errorf("group beacon id mismatch: %q != %q", group.ID, cfg.BeaconID)
	}
	if uint32(group.Threshold) != cfg.Threshold {
		return fmt.Errorf("group threshold mismatch: %d != %d", group.Threshold, cfg.Threshold)
	}
	if uint32(group.Period.Seconds()) != cfg.PeriodSeconds {
		return fmt.Errorf("group period mismatch: %d != %d", uint32(group.Period.Seconds()), cfg.PeriodSeconds)
	}
	if uint32(group.CatchupPeriod.Seconds()) != cfg.CatchupPeriodSeconds {
		return fmt.Errorf("group catchup mismatch: %d != %d", uint32(group.CatchupPeriod.Seconds()), cfg.CatchupPeriodSeconds)
	}
	if group.GenesisTime != cfg.GenesisUnix {
		return fmt.Errorf("group genesis mismatch: %d != %d", group.GenesisTime, cfg.GenesisUnix)
	}
	return nil
}

func validateChainInfoAgainstConfig(info *drandchain.Info, cfg dkgConfig) error {
	if info == nil {
		return errDKGInvalidChainInfo
	}
	if info.Scheme != cfg.Scheme {
		return fmt.Errorf("%w: chain scheme=%q expected=%q", errDKGSchemeMismatch, info.Scheme, cfg.Scheme)
	}
	if !drandcommon.CompareBeaconIDs(info.ID, cfg.BeaconID) {
		return fmt.Errorf("chain beacon id mismatch: %q != %q", info.ID, cfg.BeaconID)
	}
	if uint32(info.Period.Seconds()) != cfg.PeriodSeconds {
		return fmt.Errorf("chain period mismatch: %d != %d", uint32(info.Period.Seconds()), cfg.PeriodSeconds)
	}
	if info.GenesisTime != cfg.GenesisUnix {
		return fmt.Errorf("chain genesis mismatch: %d != %d", info.GenesisTime, cfg.GenesisUnix)
	}
	return nil
}

func loadLocalGroup(drandCfg drand.Config, cfg dkgConfig) ([]byte, *drandkey.Group, error) {
	groupBytes, err := readGroupFileBytes(drandCfg, cfg)
	if err != nil {
		return nil, nil, err
	}
	group, err := parseGroupToml(groupBytes)
	if err != nil {
		return nil, nil, err
	}
	return groupBytes, group, nil
}

func parseGroupToml(data []byte) (*drandkey.Group, error) {
	var gt drandkey.GroupTOML
	if _, err := toml.Decode(string(data), &gt); err != nil {
		return nil, fmt.Errorf("decoding group TOML: %w", err)
	}
	group := new(drandkey.Group)
	if err := group.FromTOML(&gt); err != nil {
		return nil, fmt.Errorf("parsing group TOML: %w", err)
	}
	return group, nil
}

func participantsFromGroup(group *drandkey.Group) []*dkgrpc.Participant {
	if group == nil {
		return nil
	}
	out := make([]*dkgrpc.Participant, 0, len(group.Nodes))
	for _, node := range group.Nodes {
		if node == nil || node.Identity == nil || node.Identity.Key == nil {
			continue
		}
		keyBytes, err := node.Identity.Key.MarshalBinary()
		if err != nil {
			continue
		}
		out = append(out, &dkgrpc.Participant{
			Address:   node.Identity.Addr,
			Key:       keyBytes,
			Signature: node.Identity.Signature,
		})
	}
	return out
}

func participantsFromIdentities(ids []*dkgIdentity) []*dkgrpc.Participant {
	out := make([]*dkgrpc.Participant, 0, len(ids))
	for _, id := range ids {
		if id == nil {
			continue
		}
		out = append(out, &dkgrpc.Participant{
			Address:   id.PrivateAddr,
			Key:       id.PublicKey,
			Signature: id.Signature,
		})
	}
	return out
}

func appendParticipant(existing []*dkgrpc.Participant, id *dkgIdentity) []*dkgrpc.Participant {
	if id == nil {
		return existing
	}
	for _, p := range existing {
		if p != nil && bytes.Equal(p.Key, id.PublicKey) {
			return existing
		}
	}
	return append(existing, &dkgrpc.Participant{
		Address:   id.PrivateAddr,
		Key:       id.PublicKey,
		Signature: id.Signature,
	})
}

func filterJoiners(remaining []*dkgrpc.Participant, joiners []*dkgIdentity) []*dkgIdentity {
	if len(joiners) == 0 {
		return nil
	}
	if len(remaining) == 0 {
		return joiners
	}

	out := make([]*dkgIdentity, 0, len(joiners))
	for _, joiner := range joiners {
		if joiner == nil {
			continue
		}
		if participantKeyExists(remaining, joiner.PublicKey) {
			continue
		}
		out = append(out, joiner)
	}
	return out
}

func participantKeyExists(list []*dkgrpc.Participant, key []byte) bool {
	for _, p := range list {
		if p == nil {
			continue
		}
		if bytes.Equal(p.Key, key) {
			return true
		}
	}
	return false
}

func fetchJoinerIdentities(ctx context.Context, cfg dkgConfig) ([]*dkgIdentity, error) {
	if len(cfg.JoinerAddrs) == 0 {
		return nil, nil
	}

	client := &http.Client{Timeout: 5 * time.Second}
	out := make([]*dkgIdentity, 0, len(cfg.JoinerAddrs))

	for _, addr := range cfg.JoinerAddrs {
		identity, err := fetchIdentity(ctx, client, addr)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(identity.BeaconID) != strings.TrimSpace(cfg.BeaconID) {
			return nil, fmt.Errorf("joiner beacon id mismatch: %q != %q", identity.BeaconID, cfg.BeaconID)
		}
		out = append(out, identity)
	}

	return out, nil
}

func fetchIdentity(ctx context.Context, client *http.Client, addr string) (*dkgIdentity, error) {
	base, err := normalizeHTTPBase(addr)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/vrf/v1/identity", nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("identity endpoint returned %s", resp.Status)
	}

	var payload identityResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	pubKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload.PublicKey))
	if err != nil {
		return nil, fmt.Errorf("decoding joiner public key: %w", err)
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload.Signature))
	if err != nil {
		return nil, fmt.Errorf("decoding joiner signature: %w", err)
	}

	return &dkgIdentity{
		BeaconID:    strings.TrimSpace(payload.BeaconID),
		PrivateAddr: strings.TrimSpace(payload.PrivateAddr),
		PublicKey:   pubKey,
		Signature:   signature,
	}, nil
}

func fetchGroupFileWithRetry(ctx context.Context, cfg dkgConfig, timeout time.Duration) ([]byte, *drandkey.Group, error) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	backoff := 500 * time.Millisecond
	if backoff < 100*time.Millisecond {
		backoff = 100 * time.Millisecond
	}

	client := &http.Client{Timeout: 5 * time.Second}

	for {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			return nil, nil, fmt.Errorf("timed out waiting for group file")
		}

		data, err := fetchGroupFile(ctx, client, cfg)
		if err == nil {
			group, parseErr := parseGroupToml(data)
			if parseErr != nil {
				return nil, nil, parseErr
			}
			return data, group, nil
		}

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

func fetchGroupFile(ctx context.Context, client *http.Client, cfg dkgConfig) ([]byte, error) {
	base, err := normalizeHTTPBase(cfg.GroupSourceAddr)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/vrf/v1/group", nil)
	if err != nil {
		return nil, err
	}
	if token := strings.TrimSpace(cfg.GroupSourceToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("group endpoint returned %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func normalizeHTTPBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty address")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid address %q", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return strings.TrimRight(u.String(), "/"), nil
}

func waitForDKGComplete(ctx context.Context, client dkgrpc.DKGControlClient, beaconID string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("dkg did not complete before timeout")
		}

		status, err := client.DKGStatus(ctx, &dkgrpc.DKGStatusRequest{BeaconID: beaconID})
		if err != nil {
			return err
		}
		if status.GetComplete() != nil {
			return nil
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func dkgAlreadyComplete(ctx context.Context, client dkgrpc.DKGControlClient, beaconID string) (bool, error) {
	status, err := client.DKGStatus(ctx, &dkgrpc.DKGStatusRequest{BeaconID: beaconID})
	if err != nil {
		return false, err
	}
	return status.GetComplete() != nil, nil
}

func sendAccept(ctx context.Context, client dkgrpc.DKGControlClient, beaconID string) error {
	_, err := client.Command(ctx, &dkgrpc.DKGCommand{
		Metadata: &dkgrpc.CommandMetadata{BeaconID: beaconID},
		Command:  &dkgrpc.DKGCommand_Accept{Accept: &dkgrpc.AcceptOptions{}},
	})
	if err != nil {
		return fmt.Errorf("dkg accept failed: %w", err)
	}
	return nil
}

func sendExecute(ctx context.Context, client dkgrpc.DKGControlClient, beaconID string) error {
	_, err := client.Command(ctx, &dkgrpc.DKGCommand{
		Metadata: &dkgrpc.CommandMetadata{BeaconID: beaconID},
		Command:  &dkgrpc.DKGCommand_Execute{Execute: &dkgrpc.ExecutionOptions{}},
	})
	if err != nil {
		return fmt.Errorf("dkg execute failed: %w", err)
	}
	return nil
}

func sendAbort(ctx context.Context, client dkgrpc.DKGControlClient, beaconID string) error {
	_, err := client.Command(ctx, &dkgrpc.DKGCommand{
		Metadata: &dkgrpc.CommandMetadata{BeaconID: beaconID},
		Command:  &dkgrpc.DKGCommand_Abort{Abort: &dkgrpc.AbortOptions{}},
	})
	if err != nil {
		return fmt.Errorf("dkg abort failed: %w", err)
	}
	return nil
}

func (m *dkgManager) dialControl(ctx context.Context) (*grpc.ClientConn, dkgrpc.DKGControlClient, drandgrpc.ControlClient, error) {
	addr, err := controlDialAddr(m.drandCfg.DrandControlListen)
	if err != nil {
		return nil, nil, nil, err
	}

	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(
		dialCtx,
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, nil, nil, err
	}

	return conn, dkgrpc.NewDKGControlClient(conn), drandgrpc.NewControlClient(conn), nil
}

func controlDialAddr(listen string) (string, error) {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return "", fmt.Errorf("drand control listen addr is empty")
	}

	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		if strings.HasPrefix(listen, ":") {
			return "127.0.0.1" + listen, nil
		}
		return "", err
	}

	if strings.TrimSpace(host) == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}

	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return "", fmt.Errorf("drand control host must be loopback: %s", host)
		}
	} else if !strings.EqualFold(host, "localhost") {
		return "", fmt.Errorf("drand control host must be loopback: %s", host)
	}

	return net.JoinHostPort(host, port), nil
}
