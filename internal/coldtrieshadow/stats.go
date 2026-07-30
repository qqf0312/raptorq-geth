package coldtrieshadow

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/trie"
)

func measureColdTrie(tr *trie.Trie, stateRoot common.Hash, coldRoot common.Hash, block uint64, coldKeys uint64, threshold uint64) (StoredColdTrieStats, []StoredColdTrieNode, []StoredColdPathCost, error) {
	it, err := tr.NodeIterator(nil)
	if err != nil {
		return StoredColdTrieStats{}, nil, nil, err
	}
	stats := StoredColdTrieStats{
		Version:     StatsVersion,
		BlockNumber: block,
		StateRoot:   stateRoot,
		ColdRoot:    coldRoot,
		ColdKeys:    coldKeys,
		Threshold:   threshold,
	}
	var nodes []StoredColdTrieNode
	var paths []StoredColdPathCost
	seenNodes := make(map[common.Hash]struct{})
	addNode := func(hash common.Hash, blob []byte) {
		if len(blob) == 0 {
			return
		}
		if hash == (common.Hash{}) {
			hash = crypto.Keccak256Hash(blob)
		}
		if _, ok := seenNodes[hash]; ok {
			return
		}
		seenNodes[hash] = struct{}{}
		nodes = append(nodes, StoredColdTrieNode{
			Hash: hash,
			Blob: common.CopyBytes(blob),
		})
		stats.NodeCount++
		stats.NodeBytes += uint64(len(blob))
	}
	for it.Next(true) {
		if hash := it.Hash(); hash != (common.Hash{}) {
			addNode(hash, it.NodeBlob())
		}
		if it.Leaf() {
			proof := it.LeafProof()
			var pathBytes uint64
			for _, blob := range proof {
				pathBytes += uint64(len(blob))
				addNode(common.Hash{}, blob)
			}
			paths = append(paths, StoredColdPathCost{
				Key:       common.CopyBytes(it.LeafKey()),
				NodeCount: uint64(len(proof)),
				NodeBytes: pathBytes,
			})
			stats.PathCount++
			stats.PathBytesSum += pathBytes
		}
	}
	if err := it.Error(); err != nil {
		return StoredColdTrieStats{}, nil, nil, err
	}
	return stats, nodes, paths, nil
}
