package codec

import (
	"fmt"

	cometabci "github.com/cometbft/cometbft/abci/types"
)

// ExtendedCommitCodec is the interface for encoding / decoding extended commit
// info. This is primarily used for transporting vote extensions through
// PrepareProposal/ProcessProposal into PreBlock.
type ExtendedCommitCodec interface {
	Encode(ec cometabci.ExtendedCommitInfo) ([]byte, error)
	Decode(bz []byte) (cometabci.ExtendedCommitInfo, error)
}

// DefaultExtendedCommitCodec uses CometBFT's protobuf marshal/unmarshal for
// ExtendedCommitInfo.
type DefaultExtendedCommitCodec struct{}

func NewDefaultExtendedCommitCodec() *DefaultExtendedCommitCodec {
	return &DefaultExtendedCommitCodec{}
}

func (*DefaultExtendedCommitCodec) Encode(ec cometabci.ExtendedCommitInfo) ([]byte, error) {
	bz, err := (&ec).Marshal()
	if err != nil {
		return nil, fmt.Errorf("encode extended commit info: %w", err)
	}

	return bz, nil
}

func (*DefaultExtendedCommitCodec) Decode(bz []byte) (cometabci.ExtendedCommitInfo, error) {
	var ec cometabci.ExtendedCommitInfo
	if err := (&ec).Unmarshal(bz); err != nil {
		return cometabci.ExtendedCommitInfo{}, fmt.Errorf("decode extended commit info: %w", err)
	}

	return ec, nil
}
