package coldtrieshadow

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/trie/triestate"
)

type Adapter struct {
	store     Store
	meta      *stateMetaIndex
	threshold uint64
}

func NewAdapter(store Store) *Adapter {
	return NewAdapterWithThreshold(store, DefaultThreshold)
}

func NewAdapterWithThreshold(store Store, threshold uint64) *Adapter {
	if threshold == 0 {
		threshold = DefaultThreshold
	}
	return &Adapter{
		store:     store,
		meta:      newStateMetaIndex(threshold),
		threshold: threshold,
	}
}

func (a *Adapter) OnTrieDatabaseUpdate(db *trie.Database, root common.Hash, parent common.Hash, block uint64, nodes *trienode.MergedNodeSet, states *triestate.Set) error {
	if nodes == nil {
		return nil
	}
	accountSubset, ok := nodes.Sets[common.Hash{}]
	if !ok {
		return nil
	}
	accountSubset.ForEachWithOrder(func(path string, n *trienode.Node) {
		if n == nil || !trie.IsLeafBlob(n.Blob) {
			return
		}
		key, err := trie.LeafKeyFromPathAndBlob(path, n.Blob)
		if err != nil {
			log.Warn("Failed to reconstruct cold trie leaf key", "block", block, "path", path, "err", err)
			return
		}
		if n.IsDeleted() {
			a.meta.delete(key)
			return
		}
		a.meta.access(key, block, n.Hash)
	})

	coldTrie, coldRoot, coldKeys, err := a.buildColdTrie(db, root, block)
	if err != nil {
		return err
	}
	if coldTrie == nil {
		return nil
	}
	defer coldTrie.Reset()

	stats, trieNodes, pathCosts, err := measureColdTrie(coldTrie, root, coldRoot, block, coldKeys, a.threshold)
	if err != nil {
		return err
	}
	if err := a.store.Save(block, stats, trieNodes, pathCosts); err != nil {
		return fmt.Errorf("save cold trie shadow result: %w", err)
	}
	log.Info("Cold trie shadow stored", "block", block, "stateRoot", root, "coldRoot", coldRoot, "coldKeys", coldKeys, "nodeBytes", stats.NodeBytes, "pathBytes", stats.PathBytesSum)
	return nil
}

func (a *Adapter) buildColdTrie(db *trie.Database, root common.Hash, height uint64) (*trie.Trie, common.Hash, uint64, error) {
	cold := a.meta.collectCold(height)
	if len(cold) == 0 {
		return nil, common.Hash{}, 0, nil
	}
	reader, err := db.Reader(root)
	if err != nil {
		return nil, common.Hash{}, 0, err
	}
	coldTrie := trie.NewEmpty(db)
	var inserted uint64
	for key, hash := range cold {
		blob, err := reader.Node(common.Hash{}, nil, hash)
		if err != nil || len(blob) == 0 {
			log.Warn("Cold trie source node not found", "height", height, "key", common.Bytes2Hex([]byte(key)), "hash", hash, "err", err)
			continue
		}
		value, err := trie.LeafValueFromBlob(blob)
		if err != nil {
			log.Warn("Failed to extract cold trie leaf value", "height", height, "key", common.Bytes2Hex([]byte(key)), "err", err)
			continue
		}
		if err := coldTrie.Update([]byte(key), value); err != nil {
			log.Warn("Failed to insert cold trie leaf", "height", height, "key", common.Bytes2Hex([]byte(key)), "err", err)
			continue
		}
		inserted++
	}
	if inserted == 0 {
		coldTrie.Reset()
		return nil, common.Hash{}, 0, nil
	}
	return coldTrie, coldTrie.Hash(), inserted, nil
}

var _ trie.DatabaseUpdateHook = (*Adapter)(nil)
