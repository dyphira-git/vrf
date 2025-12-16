package vrf

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/drand/drand/v2/common"
	"github.com/drand/drand/v2/crypto"

	cometabci "github.com/cometbft/cometbft/abci/types"

	"cosmossdk.io/log"
	txsigning "cosmossdk.io/x/tx/signing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"

	abcicodec "github.com/vexxvakan/vrf/x/vrf/abci/codec"
	abcitypes "github.com/vexxvakan/vrf/x/vrf/abci/types"
	"github.com/vexxvakan/vrf/x/vrf/abci/ve"
	"github.com/vexxvakan/vrf/x/vrf/emergency"
	vrfkeeper "github.com/vexxvakan/vrf/x/vrf/keeper"
	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

var (
	errNilFinalizeBlockRequest                = errors.New("vrf: received nil RequestFinalizeBlock")
	errInconsistentBeaconsInValidSet          = errors.New("vrf: inconsistent beacons in valid set")
	errInsufficientVotingPowerForValidBeacons = errors.New("vrf: insufficient voting power for valid beacons")
)

// PreBlockHandler verifies VRF vote extensions in FinalizeBlock and writes the
// canonical beacon into x/vrf state, enforcing the threshold and halt semantics
type PreBlockHandler struct {
	logger          log.Logger
	keeper          *vrfkeeper.Keeper
	accountKeeper   authkeeper.AccountKeeper
	signModeHandler *txsigning.HandlerMap
	txDecoder       sdk.TxDecoder
}

func NewPreBlockHandler(
	logger log.Logger,
	keeper *vrfkeeper.Keeper,
	accountKeeper authkeeper.AccountKeeper,
	signModeHandler *txsigning.HandlerMap,
	txDecoder sdk.TxDecoder,
) *PreBlockHandler {
	return &PreBlockHandler{
		logger:          logger.With("component", "vrf-preblock"),
		keeper:          keeper,
		accountKeeper:   accountKeeper,
		signModeHandler: signModeHandler,
		txDecoder:       txDecoder,
	}
}

// WrappedPreBlocker calls the module manager's PreBlocker and then enforces the
// VRF aggregation rules on top
func (h *PreBlockHandler) WrappedPreBlocker(mm *module.Manager) sdk.PreBlocker {
	return func(ctx sdk.Context, req *cometabci.RequestFinalizeBlock) (resp *sdk.ResponsePreBlock, err error) {
		if req == nil {
			return &sdk.ResponsePreBlock{}, errNilFinalizeBlockRequest
		}

		storeCtx := sdk.WrapSDKContext(ctx)

		// First, run all module PreBlockers
		resp, err = mm.PreBlock(ctx)
		if err != nil {
			return resp, err
		}

		// ------------------------------------------------------------------
		// Emergency disable handling
		// ------------------------------------------------------------------

		// Scan all transactions for an authorized MsgVrfEmergencyDisable
		for i, txBytes := range req.Txs {
			if len(txBytes) == 0 {
				continue
			}

			// Skip the injected extended commit info tx (it is not an SDK tx).
			if i == abcitypes.InjectedCommitInfoIndex && looksLikeInjectedCommitInfo(txBytes) {
				continue
			}

			if h.txDecoder == nil || h.signModeHandler == nil {
				break
			}

			tx, decErr := h.txDecoder(txBytes)
			if decErr != nil {
				// Decoding errors should not cause PreBlock to fail; treat this
				// transaction as non-emergency
				h.logger.Debug("vrf: failed to decode tx while checking emergency disable; ignoring", "err", decErr)
				continue
			}

			found, authorized, reason, verErr := emergency.VerifyEmergencyMsg(
				ctx,
				tx,
				h.accountKeeper,
				h.keeper,
				h.signModeHandler,
			)
			if verErr != nil {
				// Deterministically ignore invalid emergency messages at this
				// stage. The Ante handler will reject such transactions during
				// normal processing.
				h.logger.Error("vrf: emergency msg verification failed in PreBlock", "err", verErr)
				continue
			}

			if !found || !authorized {
				continue
			}

			// An authorized emergency disable has been included in this block.
			// Treat VRF as disabled for this height and persist the flag so
			// subsequent heights behave as disabled as well.
			params, paramsErr := h.keeper.GetParams(storeCtx)
			if paramsErr != nil {
				return resp, fmt.Errorf("vrf: failed to load params during emergency disable: %w", paramsErr)
			}

			if params.Enabled {
				h.logger.Info("vrf: emergency disable activated", "height", ctx.BlockHeight(), "reason", reason)
				params.Enabled = false
				if err := h.keeper.SetParams(storeCtx, params); err != nil {
					return resp, fmt.Errorf("vrf: failed to persist emergency disable params: %w", err)
				}
			}

			if err := h.keeper.SetLastBlockTime(storeCtx, ctx.BlockTime().Unix()); err != nil {
				return resp, fmt.Errorf("vrf: failed to update last block time after emergency disable: %w", err)
			}

			// VRF is disabled for this block; do not attempt to decode vote
			// extensions or enforce any VRF aggregation rules
			return resp, nil
		}

		if !ve.VoteExtensionsEnabled(ctx) {
			// Still keep last block time updated for future heights
			_ = h.keeper.SetLastBlockTime(storeCtx, ctx.BlockTime().Unix())
			return resp, nil
		}

		params, err := h.keeper.GetParams(storeCtx)
		if err != nil {
			return resp, fmt.Errorf("vrf: failed to load params in PreBlock: %w", err)
		}

		if !params.Enabled {
			_ = h.keeper.SetLastBlockTime(storeCtx, ctx.BlockTime().Unix())
			return resp, nil
		}

		lastTS, err := h.keeper.GetLastBlockTime(storeCtx)
		if err != nil {
			return resp, fmt.Errorf("vrf: failed to load last block time in PreBlock: %w", err)
		}

		tref := time.Unix(lastTS, 0).UTC()
		teff := tref.Add(-time.Duration(params.SafetyMarginSeconds) * time.Second)
		targetRound := vrftypes.RoundAt(params, teff)
		if targetRound == 0 {
			_ = h.keeper.SetLastBlockTime(storeCtx, ctx.BlockTime().Unix())
			return resp, nil
		}

		// Prepare scheme and public key for BLS verification
		scheme := crypto.NewPedersenBLSChained()
		pubKey := scheme.KeyGroup.Point()
		if err := pubKey.UnmarshalBinary(params.PublicKey); err != nil {
			return resp, fmt.Errorf("vrf: failed to unmarshal drand public key: %w", err)
		}

		extendedCommitInfo, err := loadVoteExtensions(req)
		if err != nil {
			return resp, err
		}

		totalVP := int64(0)
		validVP := int64(0)
		var chosen *vrftypes.VrfBeacon

		for _, voteInfo := range extendedCommitInfo.Votes {
			totalVP += voteInfo.Validator.Power

			if len(voteInfo.VoteExtension) == 0 {
				continue
			}

			veExt, err := ve.DecodeVrfVoteExtension(voteInfo.VoteExtension)
			if err != nil {
				h.logger.Error("vrf: failed to decode vote extension; treating as invalid", "err", err)
				continue
			}

			if veExt.DrandRound != targetRound {
				continue
			}

			hash := sha256.Sum256(veExt.Signature)
			if !bytes.Equal(hash[:], veExt.Randomness) {
				h.logger.Error("vrf: hash mismatch in PreBlock", "height", req.Height)
				continue
			}

			beacon := &common.Beacon{
				PreviousSig: veExt.PreviousSignature,
				Round:       veExt.DrandRound,
				Signature:   veExt.Signature,
			}
			if err := scheme.VerifyBeacon(beacon, pubKey); err != nil {
				h.logger.Error("vrf: BLS verification failed", "height", req.Height, "err", err)
				continue
			}

			b := vrftypes.VrfBeacon{
				DrandRound:        veExt.DrandRound,
				Randomness:        veExt.Randomness,
				Signature:         veExt.Signature,
				PreviousSignature: veExt.PreviousSignature,
			}

			if chosen == nil {
				chosen = &b
			} else if !equalBeacon(*chosen, b) {
				return resp, fmt.Errorf("%w at height %d", errInconsistentBeaconsInValidSet, ctx.BlockHeight())
			}

			validVP += voteInfo.Validator.Power
		}

		if totalVP <= 0 {
			_ = h.keeper.SetLastBlockTime(storeCtx, ctx.BlockTime().Unix())
			return resp, nil
		}

		requiredVP := (totalVP*2)/3 + 1
		if validVP < requiredVP || chosen == nil {
			h.logger.Warn(
				"vrf: did not receive enough VRF commits; waiting for more",
				"height", ctx.BlockHeight(),
				"target_round", targetRound,
				"got_power", validVP,
				"required_power", requiredVP,
				"total_power", totalVP,
			)
			return resp, fmt.Errorf("%w at height %d: got=%d required>=%d", errInsufficientVotingPowerForValidBeacons, ctx.BlockHeight(), validVP, requiredVP)
		}

		if err := h.keeper.SetLatestBeacon(storeCtx, *chosen); err != nil {
			return resp, fmt.Errorf("vrf: failed to store latest beacon: %w", err)
		}

		if err := h.keeper.SetLastBlockTime(storeCtx, ctx.BlockTime().Unix()); err != nil {
			return resp, fmt.Errorf("vrf: failed to update last block time: %w", err)
		}

		return resp, nil
	}
}

func equalBeacon(a, b vrftypes.VrfBeacon) bool {
	return a.DrandRound == b.DrandRound &&
		bytes.Equal(a.Randomness, b.Randomness) &&
		bytes.Equal(a.Signature, b.Signature) &&
		bytes.Equal(a.PreviousSignature, b.PreviousSignature)
}

func loadVoteExtensions(req *cometabci.RequestFinalizeBlock) (cometabci.ExtendedCommitInfo, error) {
	// Upstream CometBFT does not provide vote extensions in FinalizeBlock.
	// Therefore the proposer must inject ExtendedCommitInfo into the proposal txs.
	if len(req.Txs) < abcitypes.NumInjectedTxs {
		return cometabci.ExtendedCommitInfo{}, abcitypes.MissingCommitInfoError{}
	}

	injected := req.Txs[abcitypes.InjectedCommitInfoIndex]
	if !looksLikeInjectedCommitInfo(injected) {
		return cometabci.ExtendedCommitInfo{}, abcitypes.MissingCommitInfoError{}
	}

	extCommitCodec := abcicodec.NewCompressionExtendedCommitCodec(
		abcicodec.NewDefaultExtendedCommitCodec(),
		abcicodec.NewZStdCompressor(),
	)

	extendedCommitInfo, err := extCommitCodec.Decode(injected)
	if err != nil {
		return cometabci.ExtendedCommitInfo{}, abcitypes.CodecError{Err: err}
	}

	return extendedCommitInfo, nil
}

var zstdFrameMagic = [...]byte{0x28, 0xB5, 0x2F, 0xFD}

func looksLikeInjectedCommitInfo(bz []byte) bool {
	return len(bz) >= len(zstdFrameMagic) && bytes.Equal(bz[:len(zstdFrameMagic)], zstdFrameMagic[:])
}
