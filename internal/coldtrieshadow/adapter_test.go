package coldtrieshadow

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

func TestAdapterStoresColdTrieSnapshot(t *testing.T) {
	chainDB := rawdb.NewMemoryDatabase()
	coldDB := rawdb.NewMemoryDatabase()
	triedb := trie.NewDatabase(chainDB, trie.HashDefaults)
	adapter := NewAdapter(NewEthDBStore(coldDB))
	triedb.SetUpdateHook(adapter)

	tr := trie.NewEmpty(triedb)
	if err := tr.Update([]byte("account-a"), []byte("value-a")); err != nil {
		t.Fatalf("update account-a: %v", err)
	}
	root1, set1, err := tr.Commit(false)
	if err != nil {
		t.Fatalf("commit root1: %v", err)
	}
	if err := triedb.Update(root1, common.Hash{}, 1, trienode.NewWithNodeSet(set1), nil); err != nil {
		t.Fatalf("database update root1: %v", err)
	}

	tr, err = trie.New(trie.TrieID(root1), triedb)
	if err != nil {
		t.Fatalf("open root1: %v", err)
	}
	if err := tr.Update([]byte("account-b"), []byte("value-b")); err != nil {
		t.Fatalf("update account-b: %v", err)
	}
	root2, set2, err := tr.Commit(false)
	if err != nil {
		t.Fatalf("commit root2: %v", err)
	}
	if err := triedb.Update(root2, root1, 3, trienode.NewWithNodeSet(set2), nil); err != nil {
		t.Fatalf("database update root2: %v", err)
	}

	stats, err := ReadStats(coldDB, 3)
	if err != nil {
		t.Fatalf("read cold trie stats: %v", err)
	}
	if stats.StateRoot != root2 {
		t.Fatalf("state root mismatch: got %s want %s", stats.StateRoot, root2)
	}
	if stats.ColdKeys != 1 {
		t.Fatalf("cold keys mismatch: got %d want 1", stats.ColdKeys)
	}
	if stats.ColdRoot == (common.Hash{}) {
		t.Fatalf("empty cold root")
	}
	if stats.NodeBytes == 0 || stats.PathBytesSum == 0 {
		t.Fatalf("empty cost stats: nodeBytes=%d pathBytes=%d", stats.NodeBytes, stats.PathBytesSum)
	}
}
