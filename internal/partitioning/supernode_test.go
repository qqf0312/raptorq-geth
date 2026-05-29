package partitioning

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestBuildNodeReplicaMapFromPartitions(t *testing.T) {
	p0 := superPath("path-A", []string{"root", "shared", "leaf-A"}, []int{2, 1, 0})
	p1 := superPath("path-B", []string{"root", "shared", "leaf-B"}, []int{2, 1, 0})
	partitions := []Partition{
		{ID: 0, Paths: []StatePath{p0}},
		{ID: 1, Paths: []StatePath{p1}},
	}

	replicas := BuildNodeReplicaMapFromPartitions(partitions)
	shared := replicas[nodeReplicaKey(p0.Key, p0.Nodes[1])]
	if shared.ReplicaCount != 2 {
		t.Fatalf("shared replicaCount = %d, want 2", shared.ReplicaCount)
	}
	if !sameStrings(shared.Holders, []string{"partition-0", "partition-1"}) {
		t.Fatalf("shared holders = %v", shared.Holders)
	}
	leaf := replicas[nodeReplicaKey(p0.Key, p0.Nodes[2])]
	if leaf.ReplicaCount != 1 || !sameStrings(leaf.Holders, []string{"partition-0"}) {
		t.Fatalf("leaf replica info = %+v", leaf)
	}
	if len(partitions[0].Paths[0].Nodes) != 3 {
		t.Fatalf("StatePath should remain intact inside partition")
	}
}

func TestComputeNodeWeight(t *testing.T) {
	w0, err := ComputeNodeWeight(0, 1)
	if err != nil {
		t.Fatalf("w0: %v", err)
	}
	w1, _ := ComputeNodeWeight(1, 1)
	w3, _ := ComputeNodeWeight(3, 1)
	if !(w0 > w1 && w1 > w3) {
		t.Fatalf("weights should decrease: %f %f %f", w0, w1, w3)
	}
	soft, _ := ComputeNodeWeight(3, 1)
	steep, _ := ComputeNodeWeight(3, 2)
	if !(steep < soft) {
		t.Fatalf("larger beta should make replicated node weight smaller: %f >= %f", steep, soft)
	}
	if _, err := ComputeNodeWeight(-1, 1); err == nil {
		t.Fatalf("negative replicaCount should fail")
	}
	if _, err := ComputeNodeWeight(1, 0); err == nil {
		t.Fatalf("non-positive beta should fail")
	}
}

func TestComputeNodeWeights(t *testing.T) {
	path := superPath("path-A", []string{"n0", "n1", "n2"}, []int{9, 1, 0})
	replicas := map[string]NodeReplicaInfo{
		nodeReplicaKey(path.Key, path.Nodes[0]): {ReplicaCount: 1},
		nodeReplicaKey(path.Key, path.Nodes[1]): {ReplicaCount: 1},
		nodeReplicaKey(path.Key, path.Nodes[2]): {ReplicaCount: 3},
	}
	weights, err := ComputeNodeWeights([]StatePath{path}, replicas, 1)
	if err != nil {
		t.Fatalf("ComputeNodeWeights: %v", err)
	}
	if len(weights) != len(path.Nodes) {
		t.Fatalf("weights = %d, want %d", len(weights), len(path.Nodes))
	}
	if weights[0].Weight != weights[1].Weight {
		t.Fatalf("weight should depend only on replicaCount, not ChildCount/Position: %f vs %f", weights[0].Weight, weights[1].Weight)
	}
	if !(weights[1].Weight > weights[2].Weight) {
		t.Fatalf("lower replicaCount should have higher weight")
	}
}

func TestSelectHighRiskNodes(t *testing.T) {
	weights := []NodeWeight{
		nw("p2", "b", 0, 0.5),
		nw("p1", "a", 1, 1.0),
		nw("p1", "c", 0, 1.0),
		nw("p3", "d", 0, 0.25),
	}
	high, err := SelectHighRiskNodes(weights, 1)
	if err != nil {
		t.Fatalf("SelectHighRiskNodes: %v", err)
	}
	if len(high) != 2 {
		t.Fatalf("selected = %d, want 2", len(high))
	}
	if high[0].Weight != 1.0 || high[1].Weight != 1.0 {
		t.Fatalf("selected below average threshold: %+v", high)
	}
	if string(high[0].PathKey) != "p1" || high[0].Node.Position != 0 {
		t.Fatalf("tie-break should be deterministic by PathKey, Position, Hash: %+v", high)
	}
	if _, err := SelectHighRiskNodes(weights, 0); err == nil {
		t.Fatalf("bad multiplier should fail")
	}
}

func TestBuildSuperNodesFromLeaf(t *testing.T) {
	path := superPath("path-A", []string{"n0", "n1", "n2", "n3", "n4", "leaf"}, []int{1, 1, 2, 1, 1, 0})
	high := []NodeWeight{
		{PathKey: path.Key, Node: path.Nodes[4], Weight: 0.7},
		{PathKey: path.Key, Node: path.Nodes[5], Weight: 1.0},
	}
	partitions := []Partition{{ID: 7, Paths: []StatePath{path}}}

	superNodes, err := BuildSuperNodesFromLeaf([]StatePath{path}, high, partitions, 10)
	if err != nil {
		t.Fatalf("BuildSuperNodesFromLeaf: %v", err)
	}
	if len(superNodes) != 1 {
		t.Fatalf("superNodes = %d, want 1", len(superNodes))
	}
	got := superNodes[0]
	if got.StartPosition != 3 || got.EndPosition != 5 || got.ParentPosition != 2 {
		t.Fatalf("positions = start %d end %d parent %d", got.StartPosition, got.EndPosition, got.ParentPosition)
	}
	if got.ParentPartition != 7 {
		t.Fatalf("ParentPartition = %d, want 7", got.ParentPartition)
	}
	wantHashes := []string{"n3", "n4", "leaf"}
	for i := range wantHashes {
		if string(got.Nodes[i].Hash) != wantHashes[i] {
			t.Fatalf("node[%d] = %s, want %s", i, got.Nodes[i].Hash, wantHashes[i])
		}
	}
	if math.Abs(got.Weight-1.7) > 0.000001 {
		t.Fatalf("Weight = %f, want high-risk sum 1.7", got.Weight)
	}
}

func TestBuildSuperNodesStopsAtBranchingParent(t *testing.T) {
	path := superPath("path-A", []string{"n0", "n1", "leaf"}, []int{1, 2, 0})
	high := []NodeWeight{{PathKey: path.Key, Node: path.Nodes[2], Weight: 1}}
	superNodes, err := BuildSuperNodesFromLeaf([]StatePath{path}, high, nil, 10)
	if err != nil {
		t.Fatalf("BuildSuperNodesFromLeaf: %v", err)
	}
	if len(superNodes) != 1 || superNodes[0].StartPosition != 2 || len(superNodes[0].Nodes) != 1 {
		t.Fatalf("branching parent should not be merged: %+v", superNodes)
	}
}

func TestBuildSuperNodesMaxLengthAndCovered(t *testing.T) {
	path := superPath("path-A", []string{"n0", "n1", "n2", "leaf"}, []int{1, 1, 1, 0})
	high := []NodeWeight{
		{PathKey: path.Key, Node: path.Nodes[2], Weight: 0.5},
		{PathKey: path.Key, Node: path.Nodes[3], Weight: 1},
	}
	superNodes, err := BuildSuperNodesFromLeaf([]StatePath{path}, high, nil, 2)
	if err != nil {
		t.Fatalf("BuildSuperNodesFromLeaf: %v", err)
	}
	if len(superNodes) != 1 {
		t.Fatalf("covered high node should not create duplicate SuperNode, got %d", len(superNodes))
	}
	if superNodes[0].StartPosition != 2 || superNodes[0].EndPosition != 3 || len(superNodes[0].Nodes) != 2 {
		t.Fatalf("max length segment mismatch: %+v", superNodes[0])
	}
}

func TestBuildSuperNodeReallocationPlans(t *testing.T) {
	partitions := []Partition{
		{ID: 0, Paths: []StatePath{{Key: []byte("p0"), Size: 100}}},
		{ID: 1, Paths: []StatePath{{Key: []byte("p1"), Size: 10}}},
	}
	sn0 := SuperNode{PathKey: []byte("a"), StartPosition: 1, EndPosition: 2, ParentPartition: 0, OriginalHolder: "partition-0"}
	sn1 := SuperNode{PathKey: []byte("b"), StartPosition: 2, EndPosition: 3, ParentPartition: 0, OriginalHolder: "partition-0"}
	sn2 := SuperNode{PathKey: []byte("c"), StartPosition: 3, EndPosition: 4, ParentPartition: 99, OriginalHolder: "partition-0"}
	cfg := OptimizationConfig{}
	plans, err := BuildSuperNodeReallocationPlans([]SuperNode{sn2, sn1, sn0}, partitions, cfg)
	if err != nil {
		t.Fatalf("BuildSuperNodeReallocationPlans: %v", err)
	}
	if plans[0].AssignedPartition != 1 {
		t.Fatalf("secondary SuperNode should avoid primary partition: %+v", plans[0])
	}
	if plans[1].AssignedPartition != 1 {
		t.Fatalf("secondary SuperNode should not be assigned back to original holder: %+v", plans[1])
	}
	if plans[2].AssignedPartition != 1 {
		t.Fatalf("invalid primary partition should fall back to lowest-load existing partition: %+v", plans[2])
	}

	none, err := BuildSuperNodeReallocationPlans([]SuperNode{{PathKey: []byte("d"), ParentPartition: 0, OriginalHolder: "partition-0"}}, []Partition{{ID: 0}}, cfg)
	if err != nil {
		t.Fatalf("BuildSuperNodeReallocationPlans none: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("expected no secondary plan when no non-primary partition exists, got %+v", none)
	}
}

func TestOptimizeSuperNodeDistribution(t *testing.T) {
	pathA := superPath("path-A", []string{"root", "shared", "a2", "a3", "leaf-A"}, []int{1, 1, 2, 1, 0})
	pathB := superPath("path-B", []string{"root", "shared", "b2", "b3", "leaf-B"}, []int{1, 1, 2, 1, 0})
	paths := []StatePath{pathA, pathB}
	partitions := []Partition{
		{ID: 0, Paths: []StatePath{cloneStatePath(pathA)}},
		{ID: 1, Paths: []StatePath{cloneStatePath(pathB)}},
	}
	before := cloneStatePaths(partitions[0].Paths)
	cfg := OptimizationConfig{
		Beta:               1,
		HighRiskMultiplier: 1.1,
		MaxSuperNodeLength: 3,
		CandidateStorageNodes: []StorageNode{
			{ID: "partition-0", FailureDomain: "fd-a", Capacity: 10, Used: 1, Load: 0.4},
			{ID: "partition-1", FailureDomain: "fd-b", Capacity: 10, Used: 1, Load: 0.3},
		},
	}
	plans, high, superNodes, err := OptimizeSuperNodeDistribution(paths, partitions, cfg)
	if err != nil {
		t.Fatalf("OptimizeSuperNodeDistribution: %v", err)
	}
	if len(high) == 0 || len(superNodes) == 0 || len(plans) != len(superNodes) {
		t.Fatalf("unexpected outputs plans=%d high=%d superNodes=%d", len(plans), len(high), len(superNodes))
	}
	if !bytes.Equal(before[0].Key, partitions[0].Paths[0].Key) || len(partitions[0].Paths[0].Nodes) != len(before[0].Nodes) {
		t.Fatalf("partitions should not be modified")
	}
}

func TestSuperNodeOptimizationVerbose(t *testing.T) {
	pathA := superPath("account-A", []string{"n1", "n2", "n3", "n4"}, []int{2, 2, 1, 0})
	pathB := superPath("account-B", []string{"n1", "n2", "n5"}, []int{2, 2, 0})
	pathC := superPath("account-C", []string{"n1", "n6", "n7"}, []int{2, 1, 0})
	paths := []StatePath{pathA, pathB, pathC}
	partitions := []Partition{
		{ID: 0, Paths: []StatePath{pathA}},
		{ID: 1, Paths: []StatePath{pathB, pathC}},
	}
	cfg := OptimizationConfig{
		Beta:               1.2,
		HighRiskMultiplier: 1.1,
		MaxSuperNodeLength: 3,
	}
	t.Logf("first partitioning is StatePath-granular; replica scoring is PathNode-granular; reallocation is SuperNode-granular")
	t.Logf("single MPT tree under test:")
	t.Logf("  n1 -> n2 -> n3 -> n4")
	t.Logf("  n1 -> n2 -> n5")
	t.Logf("  n1 -> n6 -> n7")
	t.Logf("n1 is the root; n2 is shared by account-A/account-B; n6 is the account-C branch")
	for _, part := range partitions {
		t.Logf("partition-%d paths=%d", part.ID, len(part.Paths))
		for _, path := range part.Paths {
			t.Logf("  StatePath key=%s nodes=%d", path.Key, len(path.Nodes))
			for _, node := range path.Nodes {
				t.Logf("    node pos=%d parent=%d childCount=%d hash=%s size=%d", node.Position, node.ParentPosition, node.ChildCount, node.Hash, node.Size)
			}
		}
	}
	replicas := BuildNodeReplicaMapFromPartitions(partitions)
	if got := replicas[nodeReplicaKey(pathA.Key, pathA.Nodes[0])].ReplicaCount; got != 2 {
		t.Fatalf("n1 replicaCount = %d, want 2", got)
	}
	if got := replicas[nodeReplicaKey(pathA.Key, pathA.Nodes[1])].ReplicaCount; got != 2 {
		t.Fatalf("n2 replicaCount = %d, want 2", got)
	}
	if got := replicas[nodeReplicaKey(pathC.Key, pathC.Nodes[1])].ReplicaCount; got != 1 {
		t.Fatalf("n6 replicaCount = %d, want 1", got)
	}
	for key, info := range replicas {
		t.Logf("replica key=%s pathKey=%s pos=%d replicaCount=%d holders=%v", key, info.PathKey, info.Position, info.ReplicaCount, info.Holders)
	}
	t.Logf("weight formula: w(v)=1/(1+replicaCount(v))^beta beta=%f", cfg.Beta)
	weights, err := ComputeNodeWeights(paths, replicas, cfg.Beta)
	if err != nil {
		t.Fatalf("ComputeNodeWeights: %v", err)
	}
	var sum float64
	for _, weight := range weights {
		sum += weight.Weight
		t.Logf("node weight path=%s pos=%d hash=%s replicaCount=%d weight=%f", weight.PathKey, weight.Node.Position, weight.Node.Hash, weight.ReplicaCount, weight.Weight)
	}
	avg := sum / float64(len(weights))
	t.Logf("average weight=%f HighRiskMultiplier=%f threshold=%f", avg, cfg.HighRiskMultiplier, avg*cfg.HighRiskMultiplier)
	high, err := SelectHighRiskNodes(weights, cfg.HighRiskMultiplier)
	if err != nil {
		t.Fatalf("SelectHighRiskNodes: %v", err)
	}
	for _, weight := range high {
		t.Logf("selected high-risk node path=%s pos=%d hash=%s weight=%f", weight.PathKey, weight.Node.Position, weight.Node.Hash, weight.Weight)
	}
	t.Logf("leaf-up merge: parent can merge only when parent exists, ChildCount<=1, uncovered, same StatePath, and max length is not reached")
	superNodes, err := BuildSuperNodesFromLeaf(paths, high, partitions, cfg.MaxSuperNodeLength)
	if err != nil {
		t.Fatalf("BuildSuperNodesFromLeaf: %v", err)
	}
	for _, sn := range superNodes {
		t.Logf("SuperNode path=%s nodes=[%s] start=%d end=%d parentPos=%d parentPartition=%d originalHolder=%s weight=%f totalSize=%d", sn.PathKey, superNodeNodeHashes(sn), sn.StartPosition, sn.EndPosition, sn.ParentPosition, sn.ParentPartition, sn.OriginalHolder, sn.Weight, sn.TotalSize)
		for _, node := range sn.Nodes {
			t.Logf("  merged node pos=%d hash=%s", node.Position, node.Hash)
		}
	}
	plans, err := BuildSuperNodeReallocationPlans(superNodes, partitions, cfg)
	if err != nil {
		t.Fatalf("BuildSuperNodeReallocationPlans: %v", err)
	}
	t.Logf("FINAL SECOND-STAGE REALLOCATION PLANS:")
	for i, plan := range plans {
		t.Logf("  plan[%d]: copy SuperNode path=%s nodes=[%s] to assignedPartition=%d; parentPartition=partition-%d; reason=%s", i, plan.SuperNode.PathKey, superNodeNodeHashes(plan.SuperNode), plan.AssignedPartition, plan.PreferredPartition, plan.Reason)
		if plan.AssignedPartition < 0 || plan.AssignedPartition >= len(partitions) {
			t.Fatalf("plan assigned SuperNode outside first partition result: %+v", plan)
		}
	}
	if len(plans) == 0 {
		t.Fatalf("expected SuperNode plans")
	}
}

func superPath(key string, hashes []string, childCounts []int) StatePath {
	nodes := make([]PathNode, len(hashes))
	nodeHashes := make([][]byte, len(hashes))
	for i := range hashes {
		parent := i - 1
		if i == 0 {
			parent = -1
		}
		nodes[i] = PathNode{
			Hash:           []byte(hashes[i]),
			PathKey:        []byte(key),
			Position:       i,
			ParentPosition: parent,
			ChildCount:     childCounts[i],
			Size:           10 + i,
		}
		nodeHashes[i] = []byte(hashes[i])
	}
	return StatePath{
		Key:         []byte(key),
		PathNibbles: []byte(key),
		NodeHashes:  nodeHashes,
		Nodes:       nodes,
		LeafHash:    []byte(fmt.Sprintf("leaf-%s", key)),
		Size:        len(hashes) * 10,
	}
}

func nw(pathKey, hash string, pos int, weight float64) NodeWeight {
	return NodeWeight{
		PathKey: []byte(pathKey),
		Node: PathNode{
			Hash:           []byte(hash),
			PathKey:        []byte(pathKey),
			Position:       pos,
			ParentPosition: pos - 1,
		},
		Weight: weight,
	}
}

func superNodeNodeHashes(superNode SuperNode) string {
	hashes := make([]string, len(superNode.Nodes))
	for i := range superNode.Nodes {
		hashes[i] = string(superNode.Nodes[i].Hash)
	}
	return strings.Join(hashes, "->")
}
