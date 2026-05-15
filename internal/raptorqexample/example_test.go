package raptorqexample

import (
	"bytes"
	"testing"

	"github.com/xssnick/raptorq"
)

/*
 使用的 github.com/xssnick/raptorq 核心接口：

  - raptorq.NewRaptorQ(symbolSize)
  - CreateEncoder(data []byte)
  - Encoder.BaseSymbolsNum()
  - Encoder.GenSymbol(id uint32)
  - CreateDecoder(dataSize uint32)
  - Decoder.AddSymbol(id uint32, data []byte)
  - Decoder.Decode()

  最小调用流程：

  1. 用 raptorq.NewRaptorQ(symbolSize) 创建 codec。
  2. 用 CreateEncoder(originalBytes) 创建 encoder。
  3. 用 GenSymbol(id) 生成 source symbols，id < BaseSymbolsNum()。
  4. 用 GenSymbol(id) 生成 repair symbols，id >= BaseSymbolsNum()。
  5. 接收端保存每个 symbol 的 ID 和 Data。
  6. 用相同 symbolSize 和原始数据长度 dataSize 创建 decoder。
  7. 调用 AddSymbol(id, data) 输入收到的 symbols。
  8. 调用 Decode() 恢复原始 []byte。
*/

func TestRecoverWithRepairSymbols(t *testing.T) {
	original := bytes.Repeat([]byte("raptorq integration payload: cold trie chunk bytes; "), 240)

	decoded, err := RecoverWithRepairSymbols(original)
	if err != nil {
		t.Fatalf("recover with repair symbols: %v", err)
	}
	if !bytes.Equal(decoded, original) {
		t.Fatal("decoded payload differs from original")
	}
}

func TestRaptorQDecodeAfterSymbolLoss(t *testing.T) {
	const symbolSize = uint32(192)

	original := bytes.Repeat([]byte("larger payload for raptorq symbol-loss recovery test; "), 320)
	codec := raptorq.NewRaptorQ(symbolSize)

	// Encoder creation fixes the symbol size and original payload. GenSymbol(id)
	// returns the encoded bytes for that symbol ID.
	encoder, err := codec.CreateEncoder(original)
	if err != nil {
		t.Fatalf("create encoder: %v", err)
	}
	baseSymbols := encoder.BaseSymbolsNum()

	var received []EncodedSymbol
	lost := uint32(0)
	for id := uint32(0); id < baseSymbols; id++ {
		// Keep most source symbols, but drop a deterministic subset to simulate
		// lost packets.
		if id%7 == 0 {
			lost++
			continue
		}
		received = append(received, EncodedSymbol{ID: id, Data: encoder.GenSymbol(id)})
	}
	if lost == 0 {
		t.Fatal("test data unexpectedly produced no dropped symbols")
	}

	// Repair symbols use IDs >= baseSymbols. They carry redundant information
	// that lets the decoder recover the dropped source symbols. A few extra
	// repair symbols give the decoder enough independent equations.
	for id := baseSymbols; id < baseSymbols+lost+8; id++ {
		received = append(received, EncodedSymbol{ID: id, Data: encoder.GenSymbol(id)})
	}

	decoded, err := DecodeSymbols(symbolSize, uint32(len(original)), received)
	if err != nil {
		t.Fatalf("decode after symbol loss: %v", err)
	}
	if !bytes.Equal(decoded, original) {
		t.Fatal("decoded payload differs from original")
	}
}

func TestRaptorQDecodeFailsWithInsufficientSymbols(t *testing.T) {
	const symbolSize = uint32(128)

	original := bytes.Repeat([]byte("not enough symbols should fail; "), 180)
	codec := raptorq.NewRaptorQ(symbolSize)
	encoder, err := codec.CreateEncoder(original)
	if err != nil {
		t.Fatalf("create encoder: %v", err)
	}

	baseSymbols := encoder.BaseSymbolsNum()
	if baseSymbols < 2 {
		t.Fatalf("test needs at least two base symbols, got %d", baseSymbols)
	}

	var insufficient []EncodedSymbol
	for id := uint32(0); id < baseSymbols-1; id++ {
		insufficient = append(insufficient, EncodedSymbol{ID: id, Data: encoder.GenSymbol(id)})
	}

	_, err = DecodeSymbols(symbolSize, uint32(len(original)), insufficient)
	if err == nil {
		t.Fatal("expected decode to fail with insufficient symbols")
	}
	if got := err.Error(); got != "decode raptorq symbols: not enough symbols to decode" {
		t.Fatalf("unexpected error: %v", err)
	}
}
