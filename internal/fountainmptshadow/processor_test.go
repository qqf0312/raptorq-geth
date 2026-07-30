package fountainmptshadow

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
)

func TestProcessorFinalizeBuildsFinalAndHistoricalProofs(t *testing.T) {
	store := NewMemoryStore()
	processor, err := NewProcessor(ProcessorConfig{
		EpochStart:  5,
		Seed:        []byte("epoch-seed"),
		MinimumRows: 4,
	}, store)
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	key := []byte("key-0")
	resolver := mapPathResolver{
		pathMapKey(rootForProcessorHeight(5), key): {
			[]byte("old-branch"),
			[]byte("old-leaf"),
		},
		pathMapKey(rootForProcessorHeight(8), key): {
			[]byte("middle-branch"),
			[]byte("middle-leaf"),
		},
		pathMapKey(rootForProcessorHeight(10), key): {
			[]byte("final-branch"),
			[]byte("final-extension"),
			[]byte("final-leaf"),
		},
	}
	for height := uint64(5); height <= 10; height++ {
		var updates []KeyUpdate
		switch height {
		case 6, 9:
			updates = []KeyUpdate{{Key: key, PreviousExists: true, NewExists: true}}
		}
		if err := processor.RecordHeight(height, rootForProcessorHeight(height), updates); err != nil {
			t.Fatalf("RecordHeight(%d): %v", height, err)
		}
	}
	result, err := processor.Finalize(10, [][]byte{key}, resolver)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if result.RootCount != 6 || result.VersionCount != 3 || result.EncodedPathCount != 1 ||
		result.HistoricalLeafCount != 2 || result.AggregateProofCount != 1 {
		t.Fatalf("unexpected finalize result: %+v", result)
	}
	encoded, ok := store.EncodedPath(rootForProcessorHeight(10), key)
	if !ok {
		t.Fatal("missing final encoded path")
	}
	verified, err := VerifyEncodedPath(encoded)
	if err != nil || !verified {
		t.Fatalf("verify final path: ok=%v err=%v", verified, err)
	}
	historical, ok := store.HistoricalLeaf(rootForProcessorHeight(8), key)
	if !ok {
		t.Fatal("missing terminal-height historical leaf")
	}
	verified, err = VerifyHistoricalLeaf(historical)
	if err != nil || !verified {
		t.Fatalf("verify historical leaf: ok=%v err=%v", verified, err)
	}
	older, ok := store.HistoricalLeaf(rootForProcessorHeight(5), key)
	if !ok {
		t.Fatal("missing older historical leaf")
	}
	if encoded.Aggregate == nil || encoded.Aggregate != historical.Aggregate ||
		encoded.Aggregate != older.Aggregate {
		t.Fatal("epoch records do not share one aggregate proof")
	}
	summedQ := sumG1([]bn254.G1Affine{encoded.Q, historical.Q, older.Q})
	aggregateQs := make([]bn254.G1Affine, len(encoded.Aggregate.Segments))
	for i := range encoded.Aggregate.Segments {
		aggregateQs[i] = encoded.Aggregate.Segments[i].Q
	}
	if aggregateQ := sumG1(aggregateQs); !summedQ.Equal(&aggregateQ) {
		t.Fatal("aggregate segment commitments do not reconstruct the expected Q")
	}
	wantRelations := len(encoded.Matrix)*len(encoded.Blocks[0]) + len(historical.Block) + len(older.Block)
	if len(encoded.Aggregate.Relations) != wantRelations {
		t.Fatalf("aggregate relations=%d want one per encoded/history chunk=%d", len(encoded.Aggregate.Relations), wantRelations)
	}
	if len(encoded.Blocks[0]) < 2 {
		t.Fatal("encoded block needs two chunks for cancellation test")
	}
	var one fr.Element
	one.SetOne()
	encoded.Blocks[0][0].Add(&encoded.Blocks[0][0], &one)
	encoded.Blocks[0][1].Sub(&encoded.Blocks[0][1], &one)
	if verified, err := VerifyEncodedPath(encoded); err == nil || verified {
		t.Fatalf("aggregate accepted sum-preserving chunk tamper: verified=%v err=%v", verified, err)
	}
}

type mapPathResolver map[string][][]byte

func (r mapPathResolver) PathNodes(root common.Hash, key []byte) ([][]byte, error) {
	nodes := r[pathMapKey(root, key)]
	if len(nodes) == 0 {
		return nil, fmt.Errorf("missing path")
	}
	return nodes, nil
}

func pathMapKey(root common.Hash, key []byte) string {
	return root.Hex() + "/" + string(key)
}

func rootForProcessorHeight(height uint64) common.Hash {
	return common.BigToHash(new(big.Int).SetUint64(height))
}
