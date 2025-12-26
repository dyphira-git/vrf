package ve

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	cometabci "github.com/cometbft/cometbft/abci/types"

	"github.com/cosmos/gogoproto/proto"

	"cosmossdk.io/log"

	sdk "github.com/cosmos/cosmos-sdk/types"

	sidecarv1 "github.com/vexxvakan/vrf/api/vexxvakan/sidecar/v1"
	vetypes "github.com/vexxvakan/vrf/x/vrf/abci/ve/types"
	vrfkeeper "github.com/vexxvakan/vrf/x/vrf/keeper"
	vrfclient "github.com/vexxvakan/vrf/x/vrf/sidecar"
	vrftypes "github.com/vexxvakan/vrf/x/vrf/types"
)

var (
	errNilRequestExtendVote           = errors.New("vrf: nil RequestExtendVote")
	errNilRequestVerifyVoteExtension  = errors.New("vrf: nil RequestVerifyVoteExtension")
	errInvalidVoteExtensionFields     = errors.New("vrf: invalid vote extension fields")
	errVoteExtensionChainHashMismatch = errors.New("vrf: chain hash mismatch in vote extension")
	errVoteExtensionHashMismatch      = errors.New("vrf: randomness != SHA256(signature)")
)

func EncodeVrfVoteExtension(ve vetypes.VrfVoteExtension) ([]byte, error) {
	return proto.Marshal(&ve)
}

func DecodeVrfVoteExtension(bz []byte) (vetypes.VrfVoteExtension, error) {
	if len(bz) == 0 {
		return vetypes.VrfVoteExtension{}, nil
	}

	var ve vetypes.VrfVoteExtension
	return ve, proto.Unmarshal(bz, &ve)
}

// Handler wires the sidecar client and x/vrf keeper into ABCI++ vote extension
// handlers.
type Handler struct {
	logger  log.Logger
	client  vrfclient.Client
	keeper  *vrfkeeper.Keeper
	timeout time.Duration
}

func NewHandler(
	logger log.Logger,
	client vrfclient.Client,
	keeper *vrfkeeper.Keeper,
	timeout time.Duration,
) *Handler {
	return &Handler{
		logger:  logger.With("component", "vrf-vote-extension"),
		client:  client,
		keeper:  keeper,
		timeout: timeout,
	}
}

// ExtendVoteHandler implements the logic for fetching a drand
// beacon for target_round(H) from the sidecar and including it in the vote extension.
func (h *Handler) ExtendVoteHandler() sdk.ExtendVoteHandler {
	return func(ctx sdk.Context, req *cometabci.RequestExtendVote) (resp *cometabci.ResponseExtendVote, err error) {
		if req == nil {
			h.logger.Error("vrf: received nil RequestExtendVote; returning empty extension")
			return &cometabci.ResponseExtendVote{VoteExtension: nil}, nil
		}

		params, err := h.keeper.GetParams(ctx)
		if err != nil {
			h.logger.Error("vrf: failed to load params; returning empty extension", "err", err)
			return &cometabci.ResponseExtendVote{VoteExtension: nil}, nil
		}

		if !params.Enabled {
			h.logger.Info("vrf: params.disabled=true; returning empty extension", "height", req.Height)
			return &cometabci.ResponseExtendVote{VoteExtension: nil}, nil
		}

		tref := ctx.BlockTime().UTC()
		if tref.IsZero() {
			lastTS, err := h.keeper.GetLastBlockTime(ctx)
			if err != nil {
				h.logger.Error("vrf: failed to load last block time; returning empty extension", "err", err)
				return &cometabci.ResponseExtendVote{VoteExtension: nil}, nil
			}
			tref = time.Unix(lastTS, 0).UTC()
		}
		teff := tref.Add(-time.Duration(params.SafetyMarginSeconds) * time.Second)
		targetRound := vrftypes.RoundAt(params, teff)
		if targetRound == 0 {
			h.logger.Info(
				"vrf: target round unavailable; returning empty extension",
				"height", req.Height,
				"block_time_unix", tref.Unix(),
				"genesis_unix_sec", params.GenesisUnixSec,
				"period_seconds", params.PeriodSeconds,
				"safety_margin_seconds", params.SafetyMarginSeconds,
			)
			return &cometabci.ResponseExtendVote{VoteExtension: nil}, nil
		}

		// Call the sidecar for the specific round.
		reqCtx, cancel := context.WithTimeout(ctx, h.timeout)
		defer cancel()

		res, err := h.client.Randomness(
			ctx.WithContext(reqCtx),
			&sidecarv1.QueryRandomnessRequest{Round: targetRound},
		)
		if err != nil || res == nil {
			h.logger.Warn("vrf: failed to fetch randomness; returning empty vote extension",
				"height", req.Height,
				"round", targetRound,
				"err", err,
			)
			return &cometabci.ResponseExtendVote{VoteExtension: nil}, nil
		}

		ve := vetypes.VrfVoteExtension{
			DrandRound:        res.DrandRound,
			Randomness:        res.Randomness,
			Signature:         res.Signature,
			PreviousSignature: res.PreviousSignature,
			ChainHash:         params.ChainHash,
		}

		bz, err := EncodeVrfVoteExtension(ve)
		if err != nil {
			h.logger.Error("vrf: failed to encode vote extension; returning empty", "err", err)
			return &cometabci.ResponseExtendVote{VoteExtension: nil}, nil
		}

		return &cometabci.ResponseExtendVote{VoteExtension: bz}, nil
	}
}

// VerifyVoteExtensionHandler implements the deterministic checks:
// empty/disabled handling, basic field checks, chain hash match, and the
// cheap SHA256(signature) == randomness filter.
func (h *Handler) VerifyVoteExtensionHandler() sdk.VerifyVoteExtensionHandler {
	return func(ctx sdk.Context, req *cometabci.RequestVerifyVoteExtension) (*cometabci.ResponseVerifyVoteExtension, error) {
		if req == nil {
			h.logger.Error("vrf: received nil RequestVerifyVoteExtension; rejecting")
			return &cometabci.ResponseVerifyVoteExtension{Status: cometabci.ResponseVerifyVoteExtension_REJECT}, nil
		}

		params, err := h.keeper.GetParams(ctx)
		if err != nil {
			h.logger.Error("vrf: failed to load params in VerifyVoteExtension; rejecting", "err", err)
			return &cometabci.ResponseVerifyVoteExtension{Status: cometabci.ResponseVerifyVoteExtension_REJECT}, nil
		}

		// When VRF is enabled, require a non-empty vote extension so that votes
		// without beacons do not count towards consensus.
		if params.Enabled && len(req.VoteExtension) == 0 {
			h.logger.Warn("vrf: empty vote extension while enabled; rejecting", "height", req.Height)
			return &cometabci.ResponseVerifyVoteExtension{Status: cometabci.ResponseVerifyVoteExtension_REJECT}, nil
		}

		// Accept empty vote extensions when VRF is disabled.
		if len(req.VoteExtension) == 0 {
			return &cometabci.ResponseVerifyVoteExtension{Status: cometabci.ResponseVerifyVoteExtension_ACCEPT}, nil
		}

		// If VRF is disabled, ignore non-empty extensions for randomness purposes
		// but do not reject the vote because of VRF content.
		if !params.Enabled {
			h.logger.Info("vrf: params.disabled=true; ignoring non-empty vote extension", "height", req.Height)
			return &cometabci.ResponseVerifyVoteExtension{Status: cometabci.ResponseVerifyVoteExtension_ACCEPT}, nil
		}

		ve, err := DecodeVrfVoteExtension(req.VoteExtension)
		if err != nil {
			h.logger.Error("vrf: failed to decode vote extension", "height", req.Height, "err", err)
			return &cometabci.ResponseVerifyVoteExtension{Status: cometabci.ResponseVerifyVoteExtension_REJECT}, nil
		}

		// Basic validity.
		if ve.DrandRound == 0 || len(ve.Randomness) == 0 || len(ve.Signature) == 0 {
			err = errInvalidVoteExtensionFields
			h.logger.Error("vrf: invalid vote extension fields", "height", req.Height)
			return &cometabci.ResponseVerifyVoteExtension{Status: cometabci.ResponseVerifyVoteExtension_REJECT}, nil
		}

		// Chain hash check, if present.
		if len(ve.ChainHash) > 0 && len(params.ChainHash) > 0 {
			if string(ve.ChainHash) != string(params.ChainHash) {
				err = errVoteExtensionChainHashMismatch
				h.logger.Error("vrf: chain hash mismatch", "height", req.Height)
				return &cometabci.ResponseVerifyVoteExtension{Status: cometabci.ResponseVerifyVoteExtension_REJECT}, nil
			}
		}

		// Cheap SHA256(signature) == randomness filter.
		sigHash := sha256.Sum256(ve.Signature)
		if string(sigHash[:]) != string(ve.Randomness) {
			err = errVoteExtensionHashMismatch
			h.logger.Error("vrf: hash mismatch in vote extension", "height", req.Height)
			return &cometabci.ResponseVerifyVoteExtension{Status: cometabci.ResponseVerifyVoteExtension_REJECT}, nil
		}

		// Deterministic sanity check: compare the provided round against the
		// target round derived from the last finalized block time. Do not
		// reject here to avoid liveness regressions; PreBlock enforces the
		// strict target-round policy.
		if lastTS, tsErr := h.keeper.GetPrevBlockTime(ctx); tsErr == nil {
			if lastTS == 0 {
				lastTS, _ = h.keeper.GetLastBlockTime(ctx)
			}
			tref := time.Unix(lastTS, 0).UTC()
			teff := tref.Add(-time.Duration(params.SafetyMarginSeconds) * time.Second)
			targetRound := vrftypes.RoundAt(params, teff)

			if targetRound > 0 && ve.DrandRound != targetRound {
				h.logger.Info(
					"vrf: vote extension round does not match target round",
					"height", req.Height,
					"target_round", targetRound,
					"extension_round", ve.DrandRound,
				)
			}
		}

		return &cometabci.ResponseVerifyVoteExtension{Status: cometabci.ResponseVerifyVoteExtension_ACCEPT}, nil
	}
}
