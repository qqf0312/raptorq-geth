package fountainmptshadow

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

func TestKeyOwnerIsUniqueAndVersionIndependent(t *testing.T) {
	const nodeCount = uint64(4)
	seed := []byte("shared-owner-seed")
	keys := [][]byte{
		[]byte("key-0"),
		[]byte("key-1"),
		[]byte("key-2"),
		[]byte("key-3"),
		[]byte("key-4"),
	}
	for _, key := range keys {
		owner := keyOwner(seed, key, nodeCount)
		if owner >= nodeCount {
			t.Fatalf("key %q owner %d outside node count %d", key, owner, nodeCount)
		}
		for nodeIndex := uint64(0); nodeIndex < nodeCount; nodeIndex++ {
			adapter := &EpochAdapter{config: EpochAdapterConfig{
				Seed:      seed,
				NodeCount: nodeCount,
				NodeIndex: nodeIndex,
			}}
			if got, want := adapter.ownsKey(key), nodeIndex == owner; got != want {
				t.Fatalf("key %q node %d ownership=%t want %t", key, nodeIndex, got, want)
			}
		}
	}
}

func TestMeasureNodeUpdates(t *testing.T) {
	account := trienode.NewNodeSet(common.Hash{})
	account.AddNode([]byte{1}, trienode.New(common.HexToHash("0x1"), []byte{1, 2, 3}))
	account.AddNode([]byte{2}, trienode.NewDeleted())
	storage := trienode.NewNodeSet(common.HexToHash("0x99"))
	storage.AddNode([]byte{3}, trienode.New(common.HexToHash("0x2"), []byte{4, 5}))
	merged := trienode.NewMergedNodeSet()
	if err := merged.Merge(account); err != nil {
		t.Fatal(err)
	}
	if err := merged.Merge(storage); err != nil {
		t.Fatal(err)
	}
	stats := measureNodeUpdates(7, merged)
	if stats.Height != 7 || stats.Nodes != 2 || stats.Deletes != 1 || stats.BlobBytes != 5 || stats.HashBytes != 2*common.HashLength {
		t.Fatalf("unexpected node update stats: %+v", stats)
	}
}
