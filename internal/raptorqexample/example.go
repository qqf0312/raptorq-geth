package raptorqexample

import (
	"bytes"
	"fmt"

	"github.com/xssnick/raptorq"
)

// EncodedSymbol is the minimal metadata a receiver must keep for a RaptorQ
// symbol. The ID identifies whether Data is an original source symbol
// (ID < Encoder.BaseSymbolsNum()) or a repair symbol (ID >= BaseSymbolsNum()).
type EncodedSymbol struct {
	ID   uint32
	Data []byte
}

// RecoverWithRepairSymbols is a small integration example for
// github.com/xssnick/raptorq. It encodes data, simulates loss of a few source
// symbols, sends repair symbols, and decodes the original payload.
func RecoverWithRepairSymbols(data []byte) ([]byte, error) {
	const symbolSize = uint32(256)

	// symbolSize is the byte length of every encoded symbol. The decoder must be
	// created with the same symbol size and the original payload length.
	codec := raptorq.NewRaptorQ(symbolSize)

	// CreateEncoder splits data into source symbols internally and prepares the
	// encoder state used by GenSymbol.
	encoder, err := codec.CreateEncoder(data)
	if err != nil {
		return nil, fmt.Errorf("create raptorq encoder: %w", err)
	}
	baseSymbols := encoder.BaseSymbolsNum()

	var received []EncodedSymbol
	for id := uint32(0); id < baseSymbols; id++ {
		// Simulate packet loss by dropping some original source symbols.
		if id%5 == 0 {
			continue
		}
		received = append(received, EncodedSymbol{
			ID:   id,
			Data: encoder.GenSymbol(id),
		})
	}

	// IDs starting from BaseSymbolsNum() produce repair symbols. They are
	// generated from the same encoder state and can replace lost source symbols
	// as long as the decoder receives enough independent symbols.
	for id := baseSymbols; uint32(len(received)) < baseSymbols+4; id++ {
		received = append(received, EncodedSymbol{
			ID:   id,
			Data: encoder.GenSymbol(id),
		})
	}

	decoded, err := DecodeSymbols(symbolSize, uint32(len(data)), received)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(decoded, data) {
		return nil, fmt.Errorf("decoded payload differs from input")
	}
	return decoded, nil
}

// DecodeSymbols shows the receive-side API. The receiver needs the original
// payload length, the agreed symbol size, and every received symbol's ID.
func DecodeSymbols(symbolSize, dataSize uint32, symbols []EncodedSymbol) ([]byte, error) {
	codec := raptorq.NewRaptorQ(symbolSize)
	decoder, err := codec.CreateDecoder(dataSize)
	if err != nil {
		return nil, fmt.Errorf("create raptorq decoder: %w", err)
	}

	for _, symbol := range symbols {
		if _, err := decoder.AddSymbol(symbol.ID, symbol.Data); err != nil {
			return nil, fmt.Errorf("add raptorq symbol %d: %w", symbol.ID, err)
		}
	}

	ok, decoded, err := decoder.Decode()
	if err != nil {
		return nil, fmt.Errorf("decode raptorq symbols: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("not enough independent raptorq symbols")
	}
	return decoded, nil
}
