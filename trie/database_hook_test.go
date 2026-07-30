package trie

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/trie/triestate"
)

type testUpdateHook struct {
	calls int
}

func (h *testUpdateHook) OnTrieDatabaseUpdate(db *Database, root common.Hash, parent common.Hash, block uint64, nodes *trienode.MergedNodeSet, states *triestate.Set) error {
	h.calls++
	return nil
}

func TestDatabaseUpdateHooksFanOut(t *testing.T) {
	first := new(testUpdateHook)
	second := new(testUpdateHook)

	hooks := DatabaseUpdateHooks{first, nil, second}
	if err := hooks.OnTrieDatabaseUpdate(nil, common.Hash{}, common.Hash{}, 1, nil, nil); err != nil {
		t.Fatalf("fanout: %v", err)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("hook calls mismatch: first=%d second=%d", first.calls, second.calls)
	}
}
