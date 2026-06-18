package partitionedmptshadow

import (
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/internal/ipa"
	"github.com/ethereum/go-ethereum/internal/mptagg"
	"github.com/ethereum/go-ethereum/internal/mpttest"
	"github.com/ethereum/go-ethereum/internal/partitioning"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

func TestTrieDatabaseAdapterDrivesProcessorFromDirtyNodes(t *testing.T) {
	manager := newShadowTestManager(4)
	params, err := ipa.NewTestParams(64)
	if err != nil {
		t.Fatalf("NewTestParams: %v", err)
	}
	processor, err := NewProcessor(manager, NewMemoryStore(), mptagg.CompressOptions{Params: params})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	adapter := &TrieDatabaseAdapter{Processor: processor}

	root1, err := mpttest.BuildOrUpdateTestMPT(nil, shadowInitialMPTEntries())
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT root1: %v", err)
	}
	result1, err := adapter.OnDatabaseUpdate(root1.DB, root1.Root, types.EmptyRootHash, 1, trienode.NewWithNodeSet(root1.LastNodeSet), nil)
	if err != nil {
		t.Fatalf("OnDatabaseUpdate root1: %v", err)
	}
	if result1 == nil {
		t.Fatalf("nil root1 result")
	}
	if got := shadowPartitionSummary(result1.PartitionResult.Partitions); got != "partition-0:0x1234|partition-1:0x1256|partition-2:0x16ab|partition-3:" {
		t.Fatalf("root1 partitions = %s", got)
	}

	root2, err := mpttest.BuildOrUpdateTestMPT(root1, shadowUpdateMPTEntries())
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT root2: %v", err)
	}
	result2, err := adapter.OnDatabaseUpdate(root2.DB, root2.Root, root1.Root, 2, trienode.NewWithNodeSet(root2.LastNodeSet), nil)
	if err != nil {
		t.Fatalf("OnDatabaseUpdate root2: %v", err)
	}
	if result2 == nil {
		t.Fatalf("nil root2 result")
	}
	if len(result2.LocalViews) != len(result2.PartitionResult.RawNodeSets) {
		t.Fatalf("root2 local views = %d, raw sets = %d", len(result2.LocalViews), len(result2.PartitionResult.RawNodeSets))
	}
	for partitionID := range result2.PartitionResult.RawNodeSets {
		rawSet, ok := processor.Store().RawNodeSet(root2.Root, partitionID)
		if !ok {
			t.Fatalf("partition-%d raw set not stored", partitionID)
		}
		if !stringBoolMapsEqual(rawSet, result2.PartitionResult.RawNodeSets[partitionID]) {
			t.Fatalf("partition-%d stored raw set mismatch", partitionID)
		}
	}
}

func TestChangedAccountStatePathsFromDirtyNodesMatchesStatePathDiff(t *testing.T) {
	root1, err := mpttest.BuildOrUpdateTestMPT(nil, shadowInitialMPTEntries())
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT root1: %v", err)
	}
	root2, err := mpttest.BuildOrUpdateTestMPT(root1, shadowUpdateMPTEntries())
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT root2: %v", err)
	}
	previousPaths, err := root1.ExtractStatePaths()
	if err != nil {
		t.Fatalf("ExtractStatePaths root1: %v", err)
	}
	latestPaths, err := root2.ExtractStatePaths()
	if err != nil {
		t.Fatalf("ExtractStatePaths root2: %v", err)
	}
	want := partitioning.DiffStatePaths(previousPaths, latestPaths).Changed
	got, err := changedPathsFromTestMPTDirtyNodes(root2)
	if err != nil {
		t.Fatalf("changedPathsFromTestMPTDirtyNodes: %v", err)
	}
	if shadowPartitionSummary([]partitioning.Partition{{ID: 0, Paths: got}}) != shadowPartitionSummary([]partitioning.Partition{{ID: 0, Paths: want}}) {
		t.Fatalf("changed paths = %s, want %s",
			shadowPartitionSummary([]partitioning.Partition{{ID: 0, Paths: got}}),
			shadowPartitionSummary([]partitioning.Partition{{ID: 0, Paths: want}}))
	}
}

func changedPathsFromTestMPTDirtyNodes(tree *mpttest.TestMPT) ([]partitioning.StatePath, error) {
	view := ShadowRootView{Root: tree.Root, DB: tree.DB}
	return ChangedAccountStatePathsFromDirtyNodes(view, trienode.NewWithNodeSet(tree.LastNodeSet))
}
