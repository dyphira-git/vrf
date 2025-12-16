package codec

import (
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

type ZStdCompressor struct {
	mu  sync.Mutex
	enc *zstd.Encoder
	dec *zstd.Decoder
	err error
}

func NewZStdCompressor() *ZStdCompressor {
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		return &ZStdCompressor{err: fmt.Errorf("init zstd encoder: %w", err)}
	}

	dec, err := zstd.NewReader(nil)
	if err != nil {
		enc.Close()
		return &ZStdCompressor{err: fmt.Errorf("init zstd decoder: %w", err)}
	}

	return &ZStdCompressor{
		enc: enc,
		dec: dec,
	}
}

func (c *ZStdCompressor) Compress(bz []byte) ([]byte, error) {
	if len(bz) == 0 {
		return nil, nil
	}

	if c.err != nil {
		return nil, c.err
	}

	c.mu.Lock()
	out := c.enc.EncodeAll(bz, nil)
	c.mu.Unlock()
	return out, nil
}

func (c *ZStdCompressor) Decompress(bz []byte) ([]byte, error) {
	if len(bz) == 0 {
		return nil, nil
	}

	if c.err != nil {
		return nil, c.err
	}

	c.mu.Lock()
	out, err := c.dec.DecodeAll(bz, nil)
	c.mu.Unlock()
	return out, err
}
