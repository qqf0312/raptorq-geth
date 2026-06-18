package partitioning_test

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/internal/mpttest"
	"github.com/ethereum/go-ethereum/internal/partitioning"
)

func TestBuildPartitionsOriginalBehaviorUnchanged(t *testing.T) {
	paths := []partitioning.StatePath{
		testStatePath("C", "leaf-C", "c0", "c1"),
		testStatePath("A", "leaf-A", "a0", "a1"),
		testStatePath("B", "leaf-B", "b0", "b1"),
		testStatePath("D", "leaf-D", "d0", "d1"),
	}
	partitions, err := partitioning.BuildPartitions(paths, 2, partitioning.SortByKey)
	if err != nil {
		t.Fatalf("BuildPartitions: %v", err)
	}
	assertPartitionKeys(t, partitions[0], []string{"A", "B"})
	assertPartitionKeys(t, partitions[1], []string{"C", "D"})
	assertPartitionKeys(t, []partitioning.Partition{{Paths: paths}}[0], []string{"C", "A", "B", "D"})
}

func TestDiffStatePathsUsesTerminalLeafHash(t *testing.T) {
	oldPaths := []partitioning.StatePath{
		testStatePath("A", "leaf-A", "old-root", "old-branch", "leaf-A"),
		testStatePath("B", "leaf-B", "b0", "leaf-B"),
		testStatePath("C", "leaf-C", "c0", "leaf-C"),
	}
	newPaths := []partitioning.StatePath{
		testStatePath("A", "leaf-A", "new-root", "new-branch", "leaf-A"),
		testStatePath("B", "leaf-B2", "b0", "leaf-B2"),
		testStatePath("D", "leaf-D", "d0", "leaf-D"),
	}

	diff := partitioning.DiffStatePaths(oldPaths, newPaths)
	assertKeys(t, diff.Changed, []string{"B", "D"})
	assertKeys(t, diff.Unchanged, []string{"A"})
	assertKeys(t, diff.Removed, []string{"C"})
	if !diff.AddedNodeHashes[common.BytesToHash([]byte("new-root"))] {
		t.Fatalf("node-level diff should still track ancestor additions")
	}
	if !diff.ReusedNodeHashes[common.BytesToHash([]byte("leaf-A"))] {
		t.Fatalf("node-level diff should still track reused leaf")
	}
	if !diff.RemovedNodeHashes[common.BytesToHash([]byte("old-root"))] {
		t.Fatalf("node-level diff should still track removed ancestor")
	}
}

func TestAddLatestRootBuildPartitionsUsesChangedOnly(t *testing.T) {
	root1 := buildTestRoot(t, nil, []mpttest.TestTrieEntry{
		entryKey("A", 0x12, 0x34), entryKey("B", 0x12, 0x56), entryKey("C", 0x16, 0xab),
	})
	root2 := buildTestRoot(t, root1, []mpttest.TestTrieEntry{
		entryKey("Y", 0x18, 0xff), entryKey("Z", 0x12, 0x88),
	})
	manager := newTestManager(t, 3)
	if _, err := manager.AddLatestRoot(root1); err != nil {
		t.Fatalf("AddLatestRoot root1: %v", err)
	}
	result, err := manager.AddLatestRoot(root2)
	if err != nil {
		t.Fatalf("AddLatestRoot root2: %v", err)
	}

	assertPathNames(t, result.Diff.Changed, []string{"Z", "Y"})
	assertPathNames(t, result.Diff.Unchanged, []string{"A", "B", "C"})
	assertPartitionPathNames(t, result.Partitions, []string{"Z", "Y"})
	if len(manager.CurrentPaths) != len(result.LatestPaths) || manager.CurrentRootHash != root2.RootHashString() {
		t.Fatalf("manager current root was not updated")
	}
}

func TestBuildRawNodeSetsFlattensSuperNodes(t *testing.T) {
	pathA := testStatePath("A", "leaf-A", "a0", "a1", "a2")
	pathB := testStatePath("B", "leaf-B", "b0", "b1", "b2")
	partitions := []partitioning.Partition{
		{ID: 0, Paths: []partitioning.StatePath{pathA}},
		{ID: 1, Paths: []partitioning.StatePath{pathB}},
	}
	plans := []partitioning.SuperNodeReallocationPlan{{
		SuperNode: partitioning.SuperNode{
			PathKey:       pathB.Key,
			StartPosition: 1,
			EndPosition:   2,
			Nodes:         cloneNodesForTest(pathB.Nodes[1:3]),
		},
		AssignedPartition: 0,
	}}

	rawNodeSets := partitioning.BuildRawNodeSets(partitions, plans, nil, false)
	for _, node := range plans[0].SuperNode.Nodes {
		if !rawNodeSets[0][common.BytesToHash(node.Hash).Hex()] {
			t.Fatalf("assigned partition raw set missing flattened SuperNode hash %x", node.Hash)
		}
	}
	if rawNodeSets[1][common.BytesToHash(pathA.Nodes[0].Hash).Hex()] {
		t.Fatalf("unrelated primary path leaked into partition-1 raw set")
	}
}

func TestRootAwareManagerPartitionOutputRawNodeSets(t *testing.T) {
	root1 := buildTestRoot(t, nil, []mpttest.TestTrieEntry{
		entryKey("A", 0x12, 0x34), entryKey("B", 0x12, 0x56), entryKey("C", 0x16, 0xab),
	})
	manager := newTestManager(t, 2)
	result, err := manager.AddLatestRoot(root1)
	if err != nil {
		t.Fatalf("AddLatestRoot: %v", err)
	}
	if len(result.RawNodeSets) != 2 {
		t.Fatalf("RawNodeSets = %d, want 2", len(result.RawNodeSets))
	}
	for i, rawSet := range result.RawNodeSets {
		if len(rawSet) == 0 {
			t.Fatalf("RawNodeSets[%d] is empty", i)
		}
		for hash := range rawSet {
			if len(hash) == 0 {
				t.Fatalf("RawNodeSets[%d] has empty hash key", i)
			}
		}
	}
}

func TestNativeNodeSetUpdatedHashesMatchStatePathDiff(t *testing.T) {
	root1 := buildTestRoot(t, nil, []mpttest.TestTrieEntry{
		entryKey("A", 0x12, 0x34), entryKey("B", 0x12, 0x56), entryKey("C", 0x16, 0xab),
	})
	root2 := buildTestRoot(t, root1, []mpttest.TestTrieEntry{
		entryKey("Y", 0x18, 0xff), entryKey("Z", 0x12, 0x88),
	})
	paths1, err := root1.ExtractStatePaths()
	if err != nil {
		t.Fatalf("root1 ExtractStatePaths: %v", err)
	}
	paths2, err := root2.ExtractStatePaths()
	if err != nil {
		t.Fatalf("root2 ExtractStatePaths: %v", err)
	}
	diff := partitioning.DiffStatePaths(paths1, paths2)
	native := partitioning.UpdatedNodeHashesFromNodeSet(root2.UpdatedNodeSet())

	if len(native) != len(diff.AddedNodeHashes) {
		t.Fatalf("native updated hashes = %d, StatePath diff added hashes = %d", len(native), len(diff.AddedNodeHashes))
	}
	for hash := range native {
		if !diff.AddedNodeHashes[hash] {
			t.Fatalf("native updated hash %s missing from StatePath diff", hash)
		}
	}

	manager := newTestManager(t, 3)
	if _, err := manager.AddLatestRoot(root1); err != nil {
		t.Fatalf("AddLatestRoot root1: %v", err)
	}
	result, err := manager.AddLatestRoot(root2)
	if err != nil {
		t.Fatalf("AddLatestRoot root2 with native NodeSet: %v", err)
	}
	if len(result.Diff.AddedNodeHashes) != len(native) {
		t.Fatalf("result native-backed added hashes = %d, want %d", len(result.Diff.AddedNodeHashes), len(native))
	}
}

func newTestManager(t *testing.T, partitionCount int) *partitioning.MPTPartitionManager {
	t.Helper()
	manager, err := partitioning.NewMPTPartitionManager(partitionCount, partitioning.SortByKey, partitioning.OptimizationConfig{
		Beta:               1.2,
		HighRiskMultiplier: 1.1,
		MaxSuperNodeLength: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.PreserveLatest = false
	manager.PreserveLatestSet = true
	return manager
}

func buildTestRoot(t *testing.T, prev *mpttest.TestMPT, entries []mpttest.TestTrieEntry) *mpttest.TestMPT {
	t.Helper()
	root, err := mpttest.BuildOrUpdateTestMPT(prev, entries)
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT: %v", err)
	}
	return root
}

func entryKey(name string, bytes ...byte) mpttest.TestTrieEntry {
	return mpttest.TestTrieEntry{
		Name:  name,
		Key:   append([]byte(nil), bytes...),
		Value: []byte("value-" + name + "-kept-long-enough-to-make-real-leaf-node-standalone"),
	}
}

func testStatePath(key, leafHash string, hashes ...string) partitioning.StatePath {
	nodes := make([]partitioning.PathNode, len(hashes))
	nodeHashes := make([][]byte, len(hashes))
	for i := range hashes {
		nodes[i] = partitioning.PathNode{
			Hash:           []byte(hashes[i]),
			PathKey:        []byte(key),
			Position:       i,
			ParentPosition: i - 1,
			ChildCount:     1,
			Size:           10 + i,
		}
		if i == 0 {
			nodes[i].ParentPosition = -1
		}
		nodeHashes[i] = []byte(hashes[i])
	}
	return partitioning.StatePath{
		Key:        []byte(key),
		NodeHashes: nodeHashes,
		Nodes:      nodes,
		LeafHash:   []byte(leafHash),
		Size:       len(nodes) * 10,
	}
}

func cloneNodesForTest(nodes []partitioning.PathNode) []partitioning.PathNode {
	out := make([]partitioning.PathNode, len(nodes))
	copy(out, nodes)
	for i := range out {
		out[i].Hash = append([]byte(nil), nodes[i].Hash...)
		out[i].PathKey = append([]byte(nil), nodes[i].PathKey...)
	}
	return out
}

func assertKeys(t *testing.T, paths []partitioning.StatePath, want []string) {
	t.Helper()
	got := make([]string, len(paths))
	for i := range paths {
		got[i] = string(paths[i].Key)
	}
	if len(got) != len(want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("keys = %v, want %v", got, want)
		}
	}
}

func assertPathNames(t *testing.T, paths []partitioning.StatePath, want []string) {
	t.Helper()
	got := make([]string, len(paths))
	for i := range paths {
		got[i] = mpttest.LabelForPath(paths[i])
	}
	assertStringList(t, got, want)
}

func assertPartitionKeys(t *testing.T, partition partitioning.Partition, want []string) {
	t.Helper()
	got := make([]string, len(partition.Paths))
	for i := range partition.Paths {
		got[i] = string(partition.Paths[i].Key)
	}
	assertStringList(t, got, want)
}

func assertPartitionPathNames(t *testing.T, partitions []partitioning.Partition, want []string) {
	t.Helper()
	var got []string
	for _, part := range partitions {
		for _, path := range part.Paths {
			got = append(got, mpttest.LabelForPath(path))
		}
	}
	assertStringList(t, got, want)
}

func assertStringList(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
