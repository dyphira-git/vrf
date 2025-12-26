package chainws

import (
	"bytes"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	comettypes "github.com/cometbft/cometbft/types"

	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	gogoproto "github.com/cosmos/gogoproto/proto"
	gogoany "github.com/cosmos/gogoproto/types/any"
	protov2 "google.golang.org/protobuf/proto"

	vrfv1 "github.com/vexxvakan/vrf/api/vexxvakan/vrf/v1"
)

type txMsg struct {
	typeURL string
	msg     protov2.Message
}

func buildTxBytes(t *testing.T, msgs ...txMsg) []byte {
	t.Helper()

	var anyMsgs []*gogoany.Any
	for _, m := range msgs {
		if m.msg == nil {
			continue
		}
		b, err := protov2.Marshal(m.msg)
		if err != nil {
			t.Fatalf("marshal message: %v", err)
		}
		anyMsgs = append(anyMsgs, &gogoany.Any{
			TypeUrl: m.typeURL,
			Value:   b,
		})
	}

	body := txtypes.TxBody{Messages: anyMsgs}
	bodyBytes, err := gogoproto.Marshal(&body)
	if err != nil {
		t.Fatalf("marshal tx body: %v", err)
	}

	raw := txtypes.TxRaw{BodyBytes: bodyBytes}
	txBytes, err := gogoproto.Marshal(&raw)
	if err != nil {
		t.Fatalf("marshal tx raw: %v", err)
	}
	return txBytes
}

func TestParseInitialDKGEvent_MatchesAndExtractsFields(t *testing.T) {
	t.Parallel()

	msg := &vrfv1.MsgInitialDkg{
		Initiator:      "cosmos1init",
		ChainHash:      []byte{0xde, 0xad, 0xbe, 0xef},
		PublicKey:      []byte("pk"),
		PeriodSeconds:  3,
		GenesisUnixSec: 1700000000,
	}
	txBytes := buildTxBytes(t, txMsg{typeURL: msgInitialDkgTypeURL, msg: msg})

	infos := parseInitialDKGEvents(nil, comettypes.EventDataTx{TxResult: abci.TxResult{Height: 123, Tx: txBytes}})
	if len(infos) != 1 {
		t.Fatalf("expected 1 initial DKG event, got %d", len(infos))
	}
	info := infos[0]
	if info.Height != 123 {
		t.Fatalf("expected height=123, got %d", info.Height)
	}
	if info.Initiator != "cosmos1init" {
		t.Fatalf("expected initiator=cosmos1init, got %q", info.Initiator)
	}
	if info.ReshareEpoch != 1 {
		t.Fatalf("expected reshare_epoch=1, got %d", info.ReshareEpoch)
	}
	if info.PeriodSeconds != 3 {
		t.Fatalf("expected period_seconds=3, got %d", info.PeriodSeconds)
	}
	if info.GenesisUnixSec != 1700000000 {
		t.Fatalf("expected genesis_unix_sec=1700000000, got %d", info.GenesisUnixSec)
	}
	if !bytes.Equal(info.ChainHash, msg.ChainHash) {
		t.Fatalf("expected chain_hash=%x, got %x", msg.ChainHash, info.ChainHash)
	}
	if !bytes.Equal(info.PublicKey, msg.PublicKey) {
		t.Fatalf("expected public_key=%x, got %x", msg.PublicKey, info.PublicKey)
	}
}

func TestParseInitialDKGEvent_RejectsNonMatchingEvents(t *testing.T) {
	t.Parallel()

	txBytes := buildTxBytes(t, txMsg{
		typeURL: msgVrfEmergencyDisableTypeURL,
		msg:     &vrfv1.MsgVrfEmergencyDisable{Authority: "cosmos1auth"},
	})

	infos := parseInitialDKGEvents(nil, comettypes.EventDataTx{TxResult: abci.TxResult{Height: 1, Tx: txBytes}})
	if len(infos) != 0 {
		t.Fatal("expected parseInitialDKGEvents to reject non-matching tx")
	}
}

func TestParseReshareEvent_ExtractsFields(t *testing.T) {
	t.Parallel()

	msg := &vrfv1.MsgScheduleVrfReshare{
		Scheduler:    "cosmos1sched",
		ReshareEpoch: 2,
		Reason:       "rotation",
	}
	txBytes := buildTxBytes(t, txMsg{typeURL: msgScheduleVrfReshareTypeURL, msg: msg})

	infos := parseReshareEvents(nil, comettypes.EventDataTx{TxResult: abci.TxResult{Height: 9, Tx: txBytes}})
	if len(infos) != 1 {
		t.Fatalf("expected 1 reshare event, got %d", len(infos))
	}
	info := infos[0]
	if info.Height != 9 {
		t.Fatalf("expected height=9, got %d", info.Height)
	}
	if info.Scheduler != "cosmos1sched" {
		t.Fatalf("expected scheduler=cosmos1sched, got %q", info.Scheduler)
	}
	if info.NewEpoch != 2 {
		t.Fatalf("expected new_reshare_epoch=2, got %d", info.NewEpoch)
	}
	if info.Reason != "rotation" {
		t.Fatalf("expected reason=rotation, got %q", info.Reason)
	}
}

func TestParseParamsUpdatedEvent_ExtractsFields(t *testing.T) {
	t.Parallel()

	params := &vrfv1.VrfParams{
		Enabled:             true,
		ReshareEpoch:        7,
		PeriodSeconds:       10,
		SafetyMarginSeconds: 10,
		GenesisUnixSec:      1700000001,
		ChainHash:           []byte{0x01},
		PublicKey:           []byte("pub"),
	}
	msg := &vrfv1.MsgUpdateParams{
		Authority: "cosmos1auth",
		Params:    params,
	}
	txBytes := buildTxBytes(t, txMsg{typeURL: msgUpdateParamsTypeURL, msg: msg})

	infos := parseParamsUpdatedEvents(nil, comettypes.EventDataTx{TxResult: abci.TxResult{Height: 11, Tx: txBytes}})
	if len(infos) != 1 {
		t.Fatalf("expected 1 params update event, got %d", len(infos))
	}
	info := infos[0]
	if info.Height != 11 {
		t.Fatalf("expected height=11, got %d", info.Height)
	}
	if info.Authority != "cosmos1auth" {
		t.Fatalf("expected authority=cosmos1auth, got %q", info.Authority)
	}
	if info.Params == nil || info.Params.ReshareEpoch != params.ReshareEpoch || !info.Params.Enabled {
		t.Fatalf("unexpected params in update event")
	}
}

func TestParseEmergencyDisableEvent_ExtractsFields(t *testing.T) {
	t.Parallel()

	msg := &vrfv1.MsgVrfEmergencyDisable{
		Authority: "cosmos1auth",
		Reason:    "oops",
	}
	txBytes := buildTxBytes(t, txMsg{typeURL: msgVrfEmergencyDisableTypeURL, msg: msg})

	infos := parseEmergencyDisableEvents(nil, comettypes.EventDataTx{TxResult: abci.TxResult{Height: 42, Tx: txBytes}})
	if len(infos) != 1 {
		t.Fatalf("expected 1 emergency disable event, got %d", len(infos))
	}
	info := infos[0]
	if info.Height != 42 {
		t.Fatalf("expected height=42, got %d", info.Height)
	}
	if info.Authority != "cosmos1auth" {
		t.Fatalf("expected authority=cosmos1auth, got %q", info.Authority)
	}
	if info.Reason != "oops" {
		t.Fatalf("expected reason=oops, got %q", info.Reason)
	}
}
