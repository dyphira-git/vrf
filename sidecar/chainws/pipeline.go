package chainws

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	cometrpc "github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	comettypes "github.com/cometbft/cometbft/types"
	"go.uber.org/zap"

	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	gogoproto "github.com/cosmos/gogoproto/proto"
	gogoany "github.com/cosmos/gogoproto/types/any"
	protov2 "google.golang.org/protobuf/proto"

	vrfv1 "github.com/vexxvakan/vrf/api/vexxvakan/vrf/v1"
)

const (
	defaultSubscriberID = "vrf-sidecar"
	defaultTxBuffer     = 256
	defaultBlockBuffer  = 64

	msgInitialDkgTypeURL          = "/vexxvakan.vrf.v1.MsgInitialDkg"
	msgUpdateParamsTypeURL        = "/vexxvakan.vrf.v1.MsgUpdateParams"
	msgScheduleVrfReshareTypeURL  = "/vexxvakan.vrf.v1.MsgScheduleVrfReshare"
	msgVrfEmergencyDisableTypeURL = "/vexxvakan.vrf.v1.MsgVrfEmergencyDisable"
)

var errCometRPCAddrRequired = errors.New("comet RPC address is required")

type InitialDKGEvent struct {
	Height         int64
	Initiator      string
	ReshareEpoch   uint64
	PeriodSeconds  uint64
	GenesisUnixSec int64
	ChainHash      []byte
	PublicKey      []byte
}

type ReshareScheduledEvent struct {
	Height    int64
	Scheduler string
	OldEpoch  uint64
	NewEpoch  uint64
	Reason    string
}

type EmergencyDisableEvent struct {
	Height    int64
	Authority string
	Reason    string
}

type ParamsUpdatedEvent struct {
	Height    int64
	Authority string
	Params    *vrfv1.VrfParams
}

type NewBlockHeaderEvent struct {
	Height int64
	Time   time.Time
}

type Pipeline struct {
	logger       *zap.Logger
	cometRPCAddr string
	subscriber   string

	mu sync.RWMutex

	initialDKGHandlers     []func(context.Context, InitialDKGEvent)
	paramsUpdatedHandlers  []func(context.Context, ParamsUpdatedEvent)
	reshareHandlers        []func(context.Context, ReshareScheduledEvent)
	emergencyDisableHandle []func(context.Context, EmergencyDisableEvent)
	newBlockHeaderHandlers []func(context.Context, NewBlockHeaderEvent)

	clientMu sync.Mutex
	client   *cometrpc.HTTP

	closeOnce sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
}

func NewPipeline(ctx context.Context, cometRPCAddr string, logger *zap.Logger) (*Pipeline, error) {
	return NewPipelineWithSubscriber(ctx, cometRPCAddr, logger, defaultSubscriberID)
}

func NewPipelineWithSubscriber(ctx context.Context, cometRPCAddr string, logger *zap.Logger, subscriber string) (*Pipeline, error) {
	if strings.TrimSpace(cometRPCAddr) == "" {
		return nil, errCometRPCAddrRequired
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	subscriber = strings.TrimSpace(subscriber)
	if subscriber == "" {
		subscriber = defaultSubscriberID
	}

	internalCtx, cancel := context.WithCancel(ctx)
	p := &Pipeline{
		logger:       logger.With(zap.String("component", "sidecar-chainws")),
		cometRPCAddr: strings.TrimSpace(cometRPCAddr),
		subscriber:   subscriber,
		cancel:       cancel,
		done:         make(chan struct{}),
	}

	go p.run(internalCtx)
	return p, nil
}

func (p *Pipeline) Close() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}

		p.stopClient()
		if p.done != nil {
			<-p.done
		}
	})
}

func (p *Pipeline) OnInitialDKG(fn func(context.Context, InitialDKGEvent)) {
	if p == nil || fn == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initialDKGHandlers = append(p.initialDKGHandlers, fn)
}

func (p *Pipeline) OnReshareScheduled(fn func(context.Context, ReshareScheduledEvent)) {
	if p == nil || fn == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reshareHandlers = append(p.reshareHandlers, fn)
}

func (p *Pipeline) OnParamsUpdated(fn func(context.Context, ParamsUpdatedEvent)) {
	if p == nil || fn == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paramsUpdatedHandlers = append(p.paramsUpdatedHandlers, fn)
}

func (p *Pipeline) OnEmergencyDisable(fn func(context.Context, EmergencyDisableEvent)) {
	if p == nil || fn == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emergencyDisableHandle = append(p.emergencyDisableHandle, fn)
}

func (p *Pipeline) OnNewBlockHeader(fn func(context.Context, NewBlockHeaderEvent)) {
	if p == nil || fn == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.newBlockHeaderHandlers = append(p.newBlockHeaderHandlers, fn)
}

func (p *Pipeline) run(ctx context.Context) {
	defer func() {
		if p.done != nil {
			close(p.done)
		}
	}()

	backoff := 500 * time.Millisecond
	maxBackoff := 15 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		client, txSub, blockSub, err := p.connectAndSubscribe(ctx)
		if err != nil {
			p.logger.Warn("failed to connect chain websocket; retrying", zap.Error(err))
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		backoff = 500 * time.Millisecond
		p.setClient(client)
		p.logger.Info("chain websocket subscribed", zap.String("addr", p.cometRPCAddr))

		for {
			select {
			case <-ctx.Done():
				p.stopClient()
				return
			case ev, ok := <-txSub:
				if !ok {
					p.logger.Warn("chain websocket subscription closed; reconnecting")
					p.stopClient()
					goto reconnect
				}

				if p.handleInitialDKG(ctx, ev) {
					continue
				}

				if p.handleReshare(ctx, ev) {
					continue
				}

				if p.handleParamsUpdated(ctx, ev) {
					continue
				}

				_ = p.handleEmergencyDisable(ctx, ev)
			case ev, ok := <-blockSub:
				if !ok {
					p.logger.Warn("chain websocket subscription closed; reconnecting")
					p.stopClient()
					goto reconnect
				}

				_ = p.handleNewBlockHeader(ctx, ev)
			}
		}

	reconnect:
		continue
	}
}

func (p *Pipeline) handleInitialDKG(ctx context.Context, ev coretypes.ResultEvent) bool {
	infos := parseInitialDKGEvents(p.logger, ev.Data)
	if len(infos) == 0 {
		return false
	}

	p.mu.RLock()
	handlers := append([]func(context.Context, InitialDKGEvent){}, p.initialDKGHandlers...)
	p.mu.RUnlock()
	if len(handlers) == 0 {
		return true
	}

	go func() {
		defer p.recoverPanic("initial_dkg")
		for _, info := range infos {
			for _, h := range handlers {
				h(ctx, info)
			}
		}
	}()
	return true
}

func (p *Pipeline) handleReshare(ctx context.Context, ev coretypes.ResultEvent) bool {
	infos := parseReshareEvents(p.logger, ev.Data)
	if len(infos) == 0 {
		return false
	}

	p.mu.RLock()
	handlers := append([]func(context.Context, ReshareScheduledEvent){}, p.reshareHandlers...)
	p.mu.RUnlock()
	if len(handlers) == 0 {
		return true
	}

	go func() {
		defer p.recoverPanic("reshare")
		for _, info := range infos {
			for _, h := range handlers {
				h(ctx, info)
			}
		}
	}()
	return true
}

func (p *Pipeline) handleParamsUpdated(ctx context.Context, ev coretypes.ResultEvent) bool {
	infos := parseParamsUpdatedEvents(p.logger, ev.Data)
	if len(infos) == 0 {
		return false
	}

	p.mu.RLock()
	handlers := append([]func(context.Context, ParamsUpdatedEvent){}, p.paramsUpdatedHandlers...)
	p.mu.RUnlock()
	if len(handlers) == 0 {
		return true
	}

	go func() {
		defer p.recoverPanic("params_updated")
		for _, info := range infos {
			for _, h := range handlers {
				h(ctx, info)
			}
		}
	}()
	return true
}

func (p *Pipeline) handleEmergencyDisable(ctx context.Context, ev coretypes.ResultEvent) bool {
	infos := parseEmergencyDisableEvents(p.logger, ev.Data)
	if len(infos) == 0 {
		return false
	}

	p.mu.RLock()
	handlers := append([]func(context.Context, EmergencyDisableEvent){}, p.emergencyDisableHandle...)
	p.mu.RUnlock()
	if len(handlers) == 0 {
		return true
	}

	go func() {
		defer p.recoverPanic("emergency_disable")
		for _, info := range infos {
			for _, h := range handlers {
				h(ctx, info)
			}
		}
	}()
	return true
}

func (p *Pipeline) handleNewBlockHeader(ctx context.Context, ev coretypes.ResultEvent) bool {
	infos := parseNewBlockHeaderEvents(p.logger, ev.Data)
	if len(infos) == 0 {
		return false
	}

	p.mu.RLock()
	handlers := append([]func(context.Context, NewBlockHeaderEvent){}, p.newBlockHeaderHandlers...)
	p.mu.RUnlock()
	if len(handlers) == 0 {
		return true
	}

	go func() {
		defer p.recoverPanic("new_block_header")
		for _, info := range infos {
			for _, h := range handlers {
				h(ctx, info)
			}
		}
	}()
	return true
}

func (p *Pipeline) recoverPanic(handlerType string) {
	if r := recover(); r != nil {
		p.logger.Error("chainws handler panicked", zap.String("handler", handlerType), zap.Any("panic", r))
	}
}

func (p *Pipeline) connectAndSubscribe(ctx context.Context) (*cometrpc.HTTP, <-chan coretypes.ResultEvent, <-chan coretypes.ResultEvent, error) {
	client, err := newAndStartCometWSClient(p.cometRPCAddr)
	if err != nil {
		return nil, nil, nil, err
	}

	txSub, err := client.Subscribe(ctx, p.subscriber, "tm.event='Tx'", defaultTxBuffer)
	if err != nil {
		_ = client.Stop()
		return nil, nil, nil, err
	}

	blockSub, err := client.Subscribe(ctx, p.subscriber, "tm.event='NewBlockHeader'", defaultBlockBuffer)
	if err != nil {
		_ = client.UnsubscribeAll(context.Background(), p.subscriber)
		_ = client.Stop()
		return nil, nil, nil, err
	}

	return client, txSub, blockSub, nil
}

func (p *Pipeline) setClient(client *cometrpc.HTTP) {
	p.clientMu.Lock()
	old := p.client
	p.client = client
	p.clientMu.Unlock()

	if old != nil && old != client {
		_ = old.UnsubscribeAll(context.Background(), p.subscriber)
		_ = old.Stop()
	}
}

func (p *Pipeline) stopClient() {
	p.clientMu.Lock()
	client := p.client
	p.client = nil
	p.clientMu.Unlock()

	if client == nil {
		return
	}

	_ = client.UnsubscribeAll(context.Background(), p.subscriber)
	_ = client.Stop()
}

func newAndStartCometWSClient(cometRPCAddr string) (*cometrpc.HTTP, error) {
	paths := []string{"/websocket", "/ws"}
	var lastErr error

	for _, path := range paths {
		client, err := cometrpc.New(cometRPCAddr, path)
		if err != nil {
			lastErr = err
			continue
		}

		if err := client.Start(); err != nil {
			lastErr = err
			_ = client.Stop()
			continue
		}

		return client, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("failed to start comet websocket client (addr=%s)", cometRPCAddr)
	}
	return nil, lastErr
}

func parseInitialDKGEvents(logger *zap.Logger, data any) []InitialDKGEvent {
	height, msgs, err := extractTxMessages(data)
	if err != nil {
		logTxDecodeError(logger, "initial_dkg", err)
		return nil
	}

	var infos []InitialDKGEvent
	for _, msg := range msgs {
		if msg == nil || !typeURLMatches(msg.TypeUrl, msgInitialDkgTypeURL) {
			continue
		}
		var decoded vrfv1.MsgInitialDkg
		if err := protov2.Unmarshal(msg.Value, &decoded); err != nil {
			logMsgDecodeError(logger, "initial_dkg", err)
			continue
		}
		info := InitialDKGEvent{
			Height:         height,
			Initiator:      strings.TrimSpace(decoded.Initiator),
			ReshareEpoch:   1,
			PeriodSeconds:  decoded.PeriodSeconds,
			GenesisUnixSec: decoded.GenesisUnixSec,
			ChainHash:      append([]byte(nil), decoded.ChainHash...),
			PublicKey:      append([]byte(nil), decoded.PublicKey...),
		}
		infos = append(infos, info)
	}
	return infos
}

func parseReshareEvents(logger *zap.Logger, data any) []ReshareScheduledEvent {
	height, msgs, err := extractTxMessages(data)
	if err != nil {
		logTxDecodeError(logger, "reshare", err)
		return nil
	}

	var infos []ReshareScheduledEvent
	for _, msg := range msgs {
		if msg == nil || !typeURLMatches(msg.TypeUrl, msgScheduleVrfReshareTypeURL) {
			continue
		}
		var decoded vrfv1.MsgScheduleVrfReshare
		if err := protov2.Unmarshal(msg.Value, &decoded); err != nil {
			logMsgDecodeError(logger, "reshare", err)
			continue
		}

		info := ReshareScheduledEvent{
			Height:    height,
			Scheduler: strings.TrimSpace(decoded.Scheduler),
			NewEpoch:  decoded.ReshareEpoch,
			Reason:    strings.TrimSpace(decoded.Reason),
		}
		infos = append(infos, info)
	}
	return infos
}

func parseParamsUpdatedEvents(logger *zap.Logger, data any) []ParamsUpdatedEvent {
	height, msgs, err := extractTxMessages(data)
	if err != nil {
		logTxDecodeError(logger, "params_updated", err)
		return nil
	}

	var infos []ParamsUpdatedEvent
	for _, msg := range msgs {
		if msg == nil || !typeURLMatches(msg.TypeUrl, msgUpdateParamsTypeURL) {
			continue
		}
		var decoded vrfv1.MsgUpdateParams
		if err := protov2.Unmarshal(msg.Value, &decoded); err != nil {
			logMsgDecodeError(logger, "params_updated", err)
			continue
		}

		info := ParamsUpdatedEvent{
			Height:    height,
			Authority: strings.TrimSpace(decoded.Authority),
			Params:    decoded.Params,
		}
		infos = append(infos, info)
	}
	return infos
}

func parseEmergencyDisableEvents(logger *zap.Logger, data any) []EmergencyDisableEvent {
	height, msgs, err := extractTxMessages(data)
	if err != nil {
		logTxDecodeError(logger, "emergency_disable", err)
		return nil
	}

	var infos []EmergencyDisableEvent
	for _, msg := range msgs {
		if msg == nil || !typeURLMatches(msg.TypeUrl, msgVrfEmergencyDisableTypeURL) {
			continue
		}
		var decoded vrfv1.MsgVrfEmergencyDisable
		if err := protov2.Unmarshal(msg.Value, &decoded); err != nil {
			logMsgDecodeError(logger, "emergency_disable", err)
			continue
		}

		info := EmergencyDisableEvent{
			Height:    height,
			Authority: strings.TrimSpace(decoded.Authority),
			Reason:    strings.TrimSpace(decoded.Reason),
		}
		infos = append(infos, info)
	}
	return infos
}

func parseNewBlockHeaderEvents(_ *zap.Logger, data any) []NewBlockHeaderEvent {
	switch evt := data.(type) {
	case comettypes.EventDataNewBlockHeader:
		return []NewBlockHeaderEvent{{
			Height: evt.Header.Height,
			Time:   evt.Header.Time,
		}}
	case *comettypes.EventDataNewBlockHeader:
		if evt == nil {
			return nil
		}
		return []NewBlockHeaderEvent{{
			Height: evt.Header.Height,
			Time:   evt.Header.Time,
		}}
	case comettypes.EventDataNewBlock:
		if evt.Block == nil {
			return nil
		}
		return []NewBlockHeaderEvent{{
			Height: evt.Block.Header.Height,
			Time:   evt.Block.Header.Time,
		}}
	case *comettypes.EventDataNewBlock:
		if evt == nil || evt.Block == nil {
			return nil
		}
		return []NewBlockHeaderEvent{{
			Height: evt.Block.Header.Height,
			Time:   evt.Block.Header.Time,
		}}
	default:
		return nil
	}
}

func extractTxMessages(data any) (int64, []*gogoany.Any, error) {
	height, txBytes, ok := txBytesFromEventData(data)
	if !ok {
		return 0, nil, fmt.Errorf("chainws: event data does not contain tx bytes")
	}
	if len(txBytes) == 0 {
		return height, nil, fmt.Errorf("chainws: empty tx bytes")
	}

	var raw txtypes.TxRaw
	if err := gogoproto.Unmarshal(txBytes, &raw); err != nil {
		return height, nil, fmt.Errorf("chainws: decode tx raw: %w", err)
	}
	if len(raw.BodyBytes) == 0 {
		return height, nil, fmt.Errorf("chainws: empty tx body")
	}

	var body txtypes.TxBody
	if err := gogoproto.Unmarshal(raw.BodyBytes, &body); err != nil {
		return height, nil, fmt.Errorf("chainws: decode tx body: %w", err)
	}

	return height, body.Messages, nil
}

func txBytesFromEventData(data any) (int64, []byte, bool) {
	switch tx := data.(type) {
	case comettypes.EventDataTx:
		return tx.Height, tx.Tx, true
	case *comettypes.EventDataTx:
		return tx.Height, tx.Tx, true
	default:
		return 0, nil, false
	}
}

func typeURLMatches(got, want string) bool {
	if strings.TrimSpace(want) == "" {
		return false
	}
	return normalizeTypeURL(got) == normalizeTypeURL(want)
}

func normalizeTypeURL(typeURL string) string {
	return strings.TrimPrefix(strings.TrimSpace(typeURL), "/")
}

func logTxDecodeError(logger *zap.Logger, handler string, err error) {
	if logger == nil || err == nil {
		return
	}
	logger.Debug("chainws failed to decode tx", zap.String("handler", handler), zap.Error(err))
}

func logMsgDecodeError(logger *zap.Logger, handler string, err error) {
	if logger == nil || err == nil {
		return
	}
	logger.Warn("chainws failed to decode message", zap.String("handler", handler), zap.Error(err))
}
