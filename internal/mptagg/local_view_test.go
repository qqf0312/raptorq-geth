package mptagg

import (
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/internal/ipa"
	"github.com/ethereum/go-ethereum/internal/partitioning"
)

func TestWholePathRawWhenAllNodesOwned(t *testing.T) {
	path := testStatePath("A", 4)
	view, err := BuildLocalPartitionViewFromRawNodeSet([]partitioning.StatePath{path}, rawSetForPath(path), 0, localViewTestOptions(t))
	if err != nil {
		t.Fatalf("BuildLocalPartitionViewFromRawNodeSet: %v", err)
	}
	if len(view.AggregatedNodes) != 0 {
		t.Fatalf("aggregated node count = %d, want 0", len(view.AggregatedNodes))
	}
	if got, want := countRawViewNodes(view), len(path.Nodes); got != want {
		t.Fatalf("raw node count = %d, want %d", got, want)
	}
}

func TestIncompletePathCreatesWholePathCommitment(t *testing.T) {
	path := testStatePath("B", 5)
	rawSet := rawSetForNodes(path.Nodes[2], path.Nodes[3])
	view, err := BuildLocalPartitionViewFromRawNodeSet([]partitioning.StatePath{path}, rawSet, 0, localViewTestOptions(t))
	if err != nil {
		t.Fatalf("BuildLocalPartitionViewFromRawNodeSet: %v", err)
	}
	agg := singleAggregatedNode(t, view)
	if agg.CommitmentPrefixZeros != 0 {
		t.Fatalf("CommitmentPrefixZeros = %d, want 0", agg.CommitmentPrefixZeros)
	}
	if len(agg.OriginalNodeHashes) != len(path.Nodes) {
		t.Fatalf("committed hash count = %d, want %d", len(agg.OriginalNodeHashes), len(path.Nodes))
	}
	if got, want := attachedPositions(agg), []int{2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attached positions = %v, want %v", got, want)
	}
}

func TestAttachedRawNodesUnderWholePathCommitment(t *testing.T) {
	path := testStatePath("C", 4)
	rawSet := rawSetForNodes(path.Nodes[1], path.Nodes[3])
	view, err := BuildLocalPartitionViewFromRawNodeSet([]partitioning.StatePath{path}, rawSet, 0, localViewTestOptions(t))
	if err != nil {
		t.Fatalf("BuildLocalPartitionViewFromRawNodeSet: %v", err)
	}
	agg := singleAggregatedNode(t, view)
	if got, want := attachedPositions(agg), []int{1, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attached positions = %v, want %v", got, want)
	}
	for _, attached := range agg.AttachedRawNodes {
		if attached.Reason != "owned-raw-set" {
			t.Fatalf("attached reason = %q, want owned-raw-set", attached.Reason)
		}
	}
}

func TestMptAggDoesNotRequireSuperNodePlan(t *testing.T) {
	path := testStatePath("D", 3)
	view, err := BuildLocalPartitionViewFromRawNodeSet([]partitioning.StatePath{path}, rawSetForNodes(path.Nodes[1]), 7, localViewTestOptions(t))
	if err != nil {
		t.Fatalf("BuildLocalPartitionViewFromRawNodeSet: %v", err)
	}
	if view.PartitionID != 7 {
		t.Fatalf("PartitionID = %d, want 7", view.PartitionID)
	}
	agg := singleAggregatedNode(t, view)
	if got, want := attachedPositions(agg), []int{1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attached positions = %v, want %v", got, want)
	}
}

func TestBuildLocalPartitionViewFromRootRawNodeSetUsesAccumulatedOwnership(t *testing.T) {
	path := testStatePath("E", 4)
	root := testStatePathRoot{paths: []partitioning.StatePath{path}}
	owned := NewOwnedRawNodeSet()
	owned.Add(hex.EncodeToString(path.Nodes[2].Hash))

	view, err := BuildLocalPartitionViewFromRootRawNodeSet(root, nil, rawSetForNodes(path.Nodes[0], path.Nodes[1]), owned, 0, localViewTestOptions(t))
	if err != nil {
		t.Fatalf("BuildLocalPartitionViewFromRootRawNodeSet: %v", err)
	}
	agg := singleAggregatedNode(t, view)
	if got, want := attachedPositions(agg), []int{0, 1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attached positions = %v, want %v", got, want)
	}
	if !owned.Contains(hex.EncodeToString(path.Nodes[0].Hash)) || !owned.Contains(hex.EncodeToString(path.Nodes[1].Hash)) {
		t.Fatal("current round raw hashes were not recorded in owned set")
	}
}

type testStatePathRoot struct {
	paths []partitioning.StatePath
}

func (r testStatePathRoot) RootHashString() string {
	return "test-root"
}

func (r testStatePathRoot) ExtractStatePaths() ([]partitioning.StatePath, error) {
	out := make([]partitioning.StatePath, len(r.paths))
	copy(out, r.paths)
	return out, nil
}

func localViewTestOptions(t *testing.T) CompressOptions {
	t.Helper()
	params, err := ipa.NewTestParams(128)
	if err != nil {
		t.Fatal(err)
	}
	return CompressOptions{Params: params}
}

func testStatePath(key string, nodeCount int) partitioning.StatePath {
	nodes := make([]partitioning.PathNode, nodeCount)
	nodeHashes := make([][]byte, nodeCount)
	for i := 0; i < nodeCount; i++ {
		hash := []byte(fmt.Sprintf("%s-node-%02d", key, i))
		nodes[i] = partitioning.PathNode{
			Hash:           hash,
			PathKey:        []byte(key),
			EdgeNibble:     byte(i),
			Position:       i,
			ParentPosition: i - 1,
			ChildCount:     1,
			Size:           len(hash),
		}
		if i == nodeCount-1 {
			nodes[i].ChildCount = 0
		}
		nodeHashes[i] = append([]byte(nil), hash...)
	}
	return partitioning.StatePath{
		Key:         []byte(key),
		PathNibbles: []byte(key),
		NodeHashes:  nodeHashes,
		Nodes:       nodes,
		LeafHash:    append([]byte(nil), nodes[nodeCount-1].Hash...),
		Size:        nodeCount,
	}
}

func rawSetForPath(path partitioning.StatePath) map[string]bool {
	return rawSetForNodes(path.Nodes...)
}

func rawSetForNodes(nodes ...partitioning.PathNode) map[string]bool {
	out := make(map[string]bool, len(nodes))
	for i := range nodes {
		out[hex.EncodeToString(nodes[i].Hash)] = true
	}
	return out
}

func countRawViewNodes(view *LocalPartitionView) int {
	count := 0
	for _, node := range view.Nodes {
		if node.Kind == RawMPTNode {
			count++
		}
	}
	return count
}

func singleAggregatedNode(t *testing.T, view *LocalPartitionView) *AggregatedNode {
	t.Helper()
	if len(view.AggregatedNodes) != 1 {
		t.Fatalf("aggregated node count = %d, want 1", len(view.AggregatedNodes))
	}
	return view.AggregatedNodes[0]
}

func attachedPositions(agg *AggregatedNode) []int {
	out := make([]int, len(agg.AttachedRawNodes))
	for i := range agg.AttachedRawNodes {
		out[i] = agg.AttachedRawNodes[i].Position
	}
	return out
}
