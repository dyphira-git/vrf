package proposals

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	cometabci "github.com/cometbft/cometbft/abci/types"

	"cosmossdk.io/log"
	txsigning "cosmossdk.io/x/tx/signing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"

	"github.com/vexxvakan/vrf/x/vrf/abci/codec"
	abcitypes "github.com/vexxvakan/vrf/x/vrf/abci/types"
	"github.com/vexxvakan/vrf/x/vrf/abci/ve"
	"github.com/vexxvakan/vrf/x/vrf/emergency"
	vrfkeeper "github.com/vexxvakan/vrf/x/vrf/keeper"
)

var errInjectedExtendedCommitInfoTooLarge = errors.New("vrf: injected extended commit info exceeds MaxTxBytes")

// ProposalHandler is responsible for:
//   - Injecting the previous height's ExtendedCommitInfo into the proposal so
//     it can be deterministically consumed in PreBlock (FinalizeBlock has no
//     upstream vote-extension access).
//   - Verifying that any injected ExtendedCommitInfo is well-formed and
//     vote-extension signatures are valid in ProcessProposal.
type ProposalHandler struct {
	logger log.Logger

	// wrapped handlers
	prepareProposalHandler sdk.PrepareProposalHandler
	processProposalHandler sdk.ProcessProposalHandler

	// vrf keepers/deps (used for emergency-disable exemption and enablement)
	vrfKeeper       *vrfkeeper.Keeper
	accountKeeper   authkeeper.AccountKeeper
	signModeHandler *txsigning.HandlerMap
	txDecoder       sdk.TxDecoder

	// vote extension validation
	validateVoteExtensionsFn ve.ValidateVoteExtensionsFn

	// codecs
	extendedCommitCodec codec.ExtendedCommitCodec

	// options
	retainInjectedCommitInfoInWrappedHandler bool
}

func NewProposalHandler(
	logger log.Logger,
	prepareProposalHandler sdk.PrepareProposalHandler,
	processProposalHandler sdk.ProcessProposalHandler,
	vrfKeeper *vrfkeeper.Keeper,
	accountKeeper authkeeper.AccountKeeper,
	signModeHandler *txsigning.HandlerMap,
	txDecoder sdk.TxDecoder,
	validateVoteExtensionsFn ve.ValidateVoteExtensionsFn,
	extendedCommitCodec codec.ExtendedCommitCodec,
	opts ...Option,
) *ProposalHandler {
	h := &ProposalHandler{
		logger:                   logger.With("component", "vrf-proposals"),
		prepareProposalHandler:   prepareProposalHandler,
		processProposalHandler:   processProposalHandler,
		vrfKeeper:                vrfKeeper,
		accountKeeper:            accountKeeper,
		signModeHandler:          signModeHandler,
		txDecoder:                txDecoder,
		validateVoteExtensionsFn: validateVoteExtensionsFn,
		extendedCommitCodec:      extendedCommitCodec,
	}

	for _, opt := range opts {
		opt(h)
	}

	return h
}

func (h *ProposalHandler) PrepareProposalHandler() sdk.PrepareProposalHandler {
	return func(ctx sdk.Context, req *cometabci.RequestPrepareProposal) (resp *cometabci.ResponsePrepareProposal, err error) {
		var (
			extInfoBz                     []byte
			wrappedPrepareProposalLatency time.Duration
		)
		startTime := time.Now()

		defer func() {
			totalLatency := time.Since(startTime)
			abcitypes.RecordLatencyAndStatus(totalLatency-wrappedPrepareProposalLatency, err, abcitypes.PrepareProposal)
		}()

		if req == nil {
			err = abcitypes.NilRequestError{Handler: abcitypes.PrepareProposal}
			return nil, err
		}

		// If vote extensions are enabled, the proposer must inject the extended
		// commit info (which contains vote extensions) into the proposal txs.
		voteExtensionsEnabled := ve.VoteExtensionsEnabled(ctx)
		if voteExtensionsEnabled {
			extInfo := req.LocalLastCommit

			extInfoBz, err = h.extendedCommitCodec.Encode(extInfo)
			if err != nil {
				err = abcitypes.CodecError{Err: err}
				return &cometabci.ResponsePrepareProposal{Txs: make([][]byte, 0)}, err
			}

			abcitypes.RecordMessageSize(abcitypes.MessageExtendedCommit, len(extInfoBz))

			// Adjust req.MaxTxBytes to account for the injected tx so the wrapped
			// handler doesn't reap too many txs from the mempool.
			extInfoBzSize := int64(len(extInfoBz))
			if extInfoBzSize <= req.MaxTxBytes {
				req.MaxTxBytes -= extInfoBzSize
			} else {
				err = fmt.Errorf("%w: %d > %d", errInjectedExtendedCommitInfoTooLarge, extInfoBzSize, req.MaxTxBytes)
				return &cometabci.ResponsePrepareProposal{Txs: make([][]byte, 0)}, err
			}

			// Optionally pass the injected bytes through to wrapped handler.
			if h.retainInjectedCommitInfoInWrappedHandler {
				req.Txs = append([][]byte{extInfoBz}, req.Txs...)
			}
		}

		wrappedStart := time.Now()
		resp, err = h.prepareProposalHandler(ctx, req)
		wrappedPrepareProposalLatency = time.Since(wrappedStart)
		if err != nil {
			err = abcitypes.WrappedHandlerError{Handler: abcitypes.PrepareProposal, Err: err}
			return &cometabci.ResponsePrepareProposal{Txs: make([][]byte, 0)}, err
		}

		// Inject our tx (if extInfoBz is non-empty), and resize response to
		// respect the original max bytes (including the injected tx).
		resp.Txs = h.injectAndResize(resp.Txs, extInfoBz, req.MaxTxBytes+int64(len(extInfoBz)))

		return resp, nil
	}
}

func (h *ProposalHandler) ProcessProposalHandler() sdk.ProcessProposalHandler {
	return func(ctx sdk.Context, req *cometabci.RequestProcessProposal) (resp *cometabci.ResponseProcessProposal, err error) {
		start := time.Now()
		var wrappedProcessProposalLatency time.Duration

		defer func() {
			totalLatency := time.Since(start)
			abcitypes.RecordLatencyAndStatus(totalLatency-wrappedProcessProposalLatency, err, abcitypes.ProcessProposal)
		}()

		if req == nil {
			h.logger.Error("vrf: nil RequestProcessProposal; rejecting proposal")
			return &cometabci.ResponseProcessProposal{Status: cometabci.ResponseProcessProposal_REJECT}, nil
		}

		// Default: do not require injected data unless vote extensions are enabled
		// and VRF is enabled and not bypassed by an authorized emergency disable.
		voteExtensionsEnabled := ve.VoteExtensionsEnabled(ctx)
		vrfEnabled := h.isVrfEnabled(ctx)
		emergencyDisabled := h.hasAuthorizedEmergencyDisable(ctx, req.Txs)

		// Save injected tx so we can re-add it if a wrapped handler mutates txs.
		var injectedTx []byte

		if voteExtensionsEnabled && vrfEnabled && !emergencyDisabled {
			if len(req.Txs) < abcitypes.NumInjectedTxs {
				h.logger.Warn("vrf: missing injected commit info in ProcessProposal; rejecting proposal")
				return &cometabci.ResponseProcessProposal{Status: cometabci.ResponseProcessProposal_REJECT}, nil
			}

			extCommitBz := req.Txs[abcitypes.InjectedCommitInfoIndex]
			extInfo, decErr := h.extendedCommitCodec.Decode(extCommitBz)
			if decErr != nil {
				h.logger.Error("vrf: failed to decode injected commit info in ProcessProposal; rejecting proposal", "err", decErr)
				return &cometabci.ResponseProcessProposal{Status: cometabci.ResponseProcessProposal_REJECT}, nil
			}

			if valErr := h.validateVoteExtensionsFn(ctx, extInfo); valErr != nil {
				h.logger.Warn("vrf: invalid extended commit info in ProcessProposal; rejecting proposal", "err", valErr)
				return &cometabci.ResponseProcessProposal{Status: cometabci.ResponseProcessProposal_REJECT}, nil
			}

			abcitypes.RecordMessageSize(abcitypes.MessageExtendedCommit, len(extCommitBz))

			if !h.retainInjectedCommitInfoInWrappedHandler {
				injectedTx = req.Txs[abcitypes.InjectedCommitInfoIndex]
				req.Txs = req.Txs[abcitypes.NumInjectedTxs:]
			}
		}

		wrappedStart := time.Now()
		resp, err = h.processProposalHandler(ctx, req)
		wrappedProcessProposalLatency = time.Since(wrappedStart)
		if err != nil {
			err = abcitypes.WrappedHandlerError{Handler: abcitypes.ProcessProposal, Err: err}
		}

		if !h.retainInjectedCommitInfoInWrappedHandler && injectedTx != nil {
			req.Txs = append([][]byte{injectedTx}, req.Txs...)
		}

		return resp, err
	}
}

func (*ProposalHandler) injectAndResize(appTxs [][]byte, injectTx []byte, maxSizeBytes int64) [][]byte {
	var (
		returnedTxs   = make([][]byte, 0, len(appTxs)+1)
		consumedBytes int64
	)

	// If our injected tx isn't already present, inject it.
	if len(injectTx) != 0 && (len(appTxs) < 1 || !bytes.Equal(appTxs[0], injectTx)) {
		injectBytes := int64(len(injectTx))
		if injectBytes <= maxSizeBytes {
			consumedBytes += injectBytes
			returnedTxs = append(returnedTxs, injectTx)
		}
	}

	for _, tx := range appTxs {
		consumedBytes += int64(len(tx))
		if consumedBytes > maxSizeBytes {
			return returnedTxs
		}
		returnedTxs = append(returnedTxs, tx)
	}

	return returnedTxs
}

func (h *ProposalHandler) isVrfEnabled(ctx sdk.Context) bool {
	if h.vrfKeeper == nil {
		return false
	}

	params, err := h.vrfKeeper.GetParams(sdk.WrapSDKContext(ctx))
	if err != nil {
		h.logger.Error("vrf: failed to load params in ProcessProposal; treating as disabled", "err", err)
		return false
	}

	return params.Enabled
}

func (h *ProposalHandler) hasAuthorizedEmergencyDisable(ctx sdk.Context, txs [][]byte) bool {
	if h.txDecoder == nil || h.signModeHandler == nil || h.vrfKeeper == nil {
		return false
	}

	for i, txBytes := range txs {
		if len(txBytes) == 0 {
			continue
		}

		// Skip the injected commit info tx (it is not an SDK tx).
		if i == abcitypes.InjectedCommitInfoIndex && looksLikeInjectedCommitInfo(txBytes) {
			continue
		}

		tx, decErr := h.txDecoder(txBytes)
		if decErr != nil {
			continue
		}

		found, authorized, _, verErr := emergency.VerifyEmergencyMsg(
			ctx,
			tx,
			h.accountKeeper,
			h.vrfKeeper,
			h.signModeHandler,
		)
		if verErr != nil {
			// Deterministically ignore invalid emergency messages at this stage.
			continue
		}

		if found && authorized {
			return true
		}
	}

	return false
}

var zstdFrameMagic = [...]byte{0x28, 0xB5, 0x2F, 0xFD}

func looksLikeInjectedCommitInfo(bz []byte) bool {
	return len(bz) >= len(zstdFrameMagic) && bytes.Equal(bz[:len(zstdFrameMagic)], zstdFrameMagic[:])
}
