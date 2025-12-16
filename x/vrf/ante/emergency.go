package ante

import (
	"errors"

	txsigning "cosmossdk.io/x/tx/signing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"

	"github.com/vexxvakan/vrf/x/vrf/emergency"
	vrfkeeper "github.com/vexxvakan/vrf/x/vrf/keeper"
)

var errUnauthorizedMsgVrfEmergencyDisable = errors.New("vrf: unauthorized MsgVrfEmergencyDisable")

// EmergencyDisableDecorator is an AnteDecorator that recognizes
// MsgVrfEmergencyDisable transactions, verifies their signatures and
// allowlist authorization via VerifyEmergencyMsg, and:
//
//   - If the tx contains no MsgVrfEmergencyDisable: passes it through.
//   - If the tx contains at least one such message and is authorized:
//     treats it as gasless and bypasses sequence/nonce checks by not
//     performing any additional fee or sequence validation here.
//   - If the tx contains at least one such message but is unauthorized:
//     rejects the transaction.
//
// Actual toggling of VrfParams.Enabled is performed in PreBlock at the height
// where an authorized tx is first included.
type EmergencyDisableDecorator struct {
	accountKeeper   authkeeper.AccountKeeper
	vrfKeeper       *vrfkeeper.Keeper
	signModeHandler *txsigning.HandlerMap
}

func NewEmergencyDisableDecorator(
	accountKeeper authkeeper.AccountKeeper,
	vrfKeeper *vrfkeeper.Keeper,
	signModeHandler *txsigning.HandlerMap,
) EmergencyDisableDecorator {
	return EmergencyDisableDecorator{
		accountKeeper:   accountKeeper,
		vrfKeeper:       vrfKeeper,
		signModeHandler: signModeHandler,
	}
}

func (d EmergencyDisableDecorator) AnteHandle(
	ctx sdk.Context,
	tx sdk.Tx,
	simulate bool,
	next sdk.AnteHandler,
) (sdk.Context, error) {
	// During simulation we bypass the emergency decorator entirely to avoid
	// unexpected failures due to missing accounts or pubkeys.
	if simulate {
		return next(ctx, tx, simulate)
	}

	found, authorized, _, err := emergency.VerifyEmergencyMsg(
		ctx,
		tx,
		d.accountKeeper,
		d.vrfKeeper,
		d.signModeHandler,
	)
	if err != nil {
		// Invalid emergency messages are rejected by the Ante handler, which
		// keeps behavior aligned with PreBlock (which will also treat them as
		// unauthorized).
		return ctx, err
	}

	if !found {
		// Not an emergency disable tx; pass through to the rest of the ante
		// chain as usual.
		return next(ctx, tx, simulate)
	}

	if !authorized {
		return ctx, errUnauthorizedMsgVrfEmergencyDisable
	}

	// Transaction contains an authorized MsgVrfEmergencyDisable. The PRD
	// specifies that it should be gasless and bypass sequence/nonce checks.
	// We honor this by short-circuiting the ante chain after performing our
	// own signature verification.
	return ctx, nil
}
