package coldtrieshadow

import "github.com/ethereum/go-ethereum/common"

const (
	StatsVersion     uint64 = 1
	DefaultThreshold uint64 = 2
	defaultAccessF   uint64 = 10
)

// StoredColdTrieStats records one cold trie snapshot and its storage cost.
type StoredColdTrieStats struct {
	Version      uint64
	BlockNumber  uint64
	StateRoot    common.Hash
	ColdRoot     common.Hash
	ColdKeys     uint64
	NodeCount    uint64
	NodeBytes    uint64
	PathCount    uint64
	PathBytesSum uint64
	Threshold    uint64
}

// StoredColdPathCost records the root-to-leaf path cost for one cold key.
type StoredColdPathCost struct {
	Key       []byte
	NodeCount uint64
	NodeBytes uint64
}

// StoredColdTrieNode records one standalone cold trie node.
type StoredColdTrieNode struct {
	Hash common.Hash
	Blob []byte
}
