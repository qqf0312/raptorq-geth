package coldtrieshadow

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
)

func TestEthDBStoreSavesStatsAndNodes(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	store := NewEthDBStore(db)

	coldRoot := common.HexToHash("0x01")
	nodeHash := common.HexToHash("0x02")
	stats := StoredColdTrieStats{
		Version:      StatsVersion,
		BlockNumber:  7,
		StateRoot:    common.HexToHash("0x03"),
		ColdRoot:     coldRoot,
		ColdKeys:     2,
		NodeCount:    1,
		NodeBytes:    3,
		PathCount:    2,
		PathBytesSum: 6,
		Threshold:    DefaultThreshold,
	}
	nodes := []StoredColdTrieNode{{Hash: nodeHash, Blob: []byte{1, 2, 3}}}
	paths := []StoredColdPathCost{{Key: []byte("a"), NodeCount: 1, NodeBytes: 3}}

	if err := store.Save(stats.BlockNumber, stats, nodes, paths); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := ReadStats(db, stats.BlockNumber)
	if err != nil {
		t.Fatalf("read stats: %v", err)
	}
	if *got != stats {
		t.Fatalf("stats mismatch: got %#v want %#v", *got, stats)
	}
	if blob := ReadNode(db, coldRoot, nodeHash); !bytes.Equal(blob, nodes[0].Blob) {
		t.Fatalf("node blob mismatch: got %x want %x", blob, nodes[0].Blob)
	}
}
