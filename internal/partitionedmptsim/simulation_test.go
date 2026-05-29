package partitionedmptsim

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/internal/ipa"
	"github.com/ethereum/go-ethereum/internal/mptagg"
	"github.com/ethereum/go-ethereum/internal/mpttest"
	"github.com/ethereum/go-ethereum/internal/partitioning"
)

func initialMPTEntries() []mpttest.TestTrieEntry {
	return []mpttest.TestTrieEntry{
		{Name: "A", Key: []byte{0x12, 0x34}, Value: simLongValue("A-v1")},
		{Name: "B", Key: []byte{0x12, 0x56}, Value: simLongValue("B-v1")},
		{Name: "C", Key: []byte{0x16, 0xab}, Value: simLongValue("C-v1")},
	}
}

func updateMPTEntries() []mpttest.TestTrieEntry {
	return []mpttest.TestTrieEntry{
		{Name: "Z", Key: []byte{0x12, 0x88}, Value: simLongValue("Z-v2")},
		{Name: "Y", Key: []byte{0x18, 0xff}, Value: simLongValue("Y-v2")},
	}
}

func simLongValue(label string) []byte {
	return []byte("value-" + label + "-kept-long-enough-to-make-leaf-nodes-standalone")
}

func logTestEntries(t *testing.T, entries []mpttest.TestTrieEntry) {
	t.Helper()
	for _, entry := range entries {
		t.Logf("  %s key=0x%x", entry.Name, entry.Key)
	}
}

func assertPathLabels(t *testing.T, paths []partitioning.StatePath, labels []string) {
	t.Helper()
	got := make(map[string]bool)
	for _, path := range paths {
		got[mpttest.LabelForPath(path)] = true
	}
	for _, label := range labels {
		if !got[label] {
			t.Fatalf("missing path label %s; got %v", label, got)
		}
	}
	if len(got) != len(labels) {
		t.Fatalf("path labels = %v, want %v", got, labels)
	}
}

func TestAddLatestMPTInitialBuild(t *testing.T) {
	version := MPTVersion{
		Height: 1,
		RootID: "root-1",
		Paths: []partitioning.StatePath{
			simStatePath("account-A", "n1", "n2", "n3"),
			simStatePath("account-B", "n1", "n2", "n4"),
			simStatePath("account-C", "n1", "n5", "n6"),
		},
	}
	before := simPathsSummary(version.Paths)

	state, err := AddLatestMPTAndUpdateForest(nil, version, 2, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("AddLatestMPTAndUpdateForest: %v", err)
	}
	if len(state.Versions) != 1 {
		t.Fatalf("versions = %d, want 1", len(state.Versions))
	}
	if state.LatestHeight != 1 {
		t.Fatalf("LatestHeight = %d, want 1", state.LatestHeight)
	}
	if len(state.Partitions) != 2 {
		t.Fatalf("partitions = %d, want 2", len(state.Partitions))
	}
	for id := 0; id < 2; id++ {
		if state.LocalViews[id] == nil {
			t.Fatalf("missing LocalView for partition-%d", id)
		}
	}
	if after := simPathsSummary(version.Paths); after != before {
		t.Fatalf("newVersion.Paths mutated: before=%s after=%s", before, after)
	}
}

func TestNewPathUsesKeyPartitionNotOldNodeAffinity(t *testing.T) {
	oldPath := simStatePath("A", "a", "b", "c")
	oldState := &MPTForestState{
		Versions:     []MPTVersion{{Height: 1, RootID: "root-1", Paths: []partitioning.StatePath{oldPath}}},
		LatestHeight: 1,
		Partitions: []partitioning.Partition{
			{ID: 0, Paths: []partitioning.StatePath{oldPath}},
			{ID: 1},
			{ID: 2},
		},
	}
	newPath := simStatePath("Z", "x", "b", "c")
	selected, err := AssignNewPathByKey(newPath, 3, partitioning.SortByKey)
	if err != nil {
		t.Fatalf("AssignNewPathByKey: %v", err)
	}
	if selected != 2 {
		t.Fatalf("test setup expected key Z to map to partition-2, got %d", selected)
	}
	state, err := AddLatestMPTByKeyPartition(oldState, MPTVersion{Height: 2, RootID: "root-2", Paths: []partitioning.StatePath{newPath}}, 3, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("AddLatestMPTByKeyPartition: %v", err)
	}
	if len(state.Partitions) != 3 {
		t.Fatalf("partition count changed to %d", len(state.Partitions))
	}
	if !partitionHasPath(state.Partitions[0], "A") {
		t.Fatalf("oldPath should remain in partition-0")
	}
	if partitionHasPath(state.Partitions[0], "Z") {
		t.Fatalf("newPath reused b/c but should not inherit partition-0")
	}
	if !partitionHasPath(state.Partitions[2], "Z") {
		t.Fatalf("newPath should be appended to key-selected partition-2")
	}
}

func TestIncrementalUpdateDoesNotRepartitionOldPaths(t *testing.T) {
	oldA := simStatePath("old-A", "a0", "a1")
	oldB := simStatePath("old-B", "b0", "b1")
	oldState := &MPTForestState{
		Versions:     []MPTVersion{{Height: 1, RootID: "root-1", Paths: []partitioning.StatePath{oldA, oldB}}},
		LatestHeight: 1,
		Partitions: []partitioning.Partition{
			{ID: 0, Paths: []partitioning.StatePath{oldA}},
			{ID: 1, Paths: []partitioning.StatePath{oldB}},
			{ID: 2},
		},
	}
	beforePartitions := simPartitionsSummary(oldState.Partitions)
	beforeVersions := simVersionSummary(oldState.Versions)

	newPath := simStatePath("Z", "x0", "a1")
	state, err := AddLatestMPTByKeyPartition(oldState, MPTVersion{Height: 2, RootID: "root-2", Paths: []partitioning.StatePath{newPath}}, 3, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("AddLatestMPTByKeyPartition: %v", err)
	}
	if len(state.Partitions) != 3 {
		t.Fatalf("partition count changed to %d", len(state.Partitions))
	}
	if !partitionHasPath(state.Partitions[0], "old-A") || !partitionHasPath(state.Partitions[1], "old-B") {
		t.Fatalf("old path assignment changed: %s", simPartitionsSummary(state.Partitions))
	}
	if partitionHasPath(state.Partitions[0], "Z") || partitionHasPath(state.Partitions[1], "Z") {
		t.Fatalf("new path should not be appended to old partitions: %s", simPartitionsSummary(state.Partitions))
	}
	if !partitionHasPath(state.Partitions[2], "Z") {
		t.Fatalf("new path should be appended to key-selected partition-2")
	}
	if after := simPartitionsSummary(oldState.Partitions); after != beforePartitions {
		t.Fatalf("oldState.Partitions mutated: before=%s after=%s", beforePartitions, after)
	}
	if after := simVersionSummary(oldState.Versions); after != beforeVersions {
		t.Fatalf("oldState.Versions mutated: before=%s after=%s", beforeVersions, after)
	}
}

func TestIncrementalUpdateOnlyAddsLatestSuperNodePlans(t *testing.T) {
	cfg := simOptimizationConfig()
	cfg.HighRiskMultiplier = 0.9
	first := MPTVersion{
		Height: 1,
		RootID: "root-1",
		Paths: []partitioning.StatePath{
			simStatePathWithChildCounts("A", []string{"a1", "a2", "a3"}, []int{1, 1, 0}),
		},
	}
	initial, err := AddLatestMPTByKeyPartition(nil, first, 3, partitioning.SortByKey, cfg)
	if err != nil {
		t.Fatalf("initial AddLatestMPTByKeyPartition: %v", err)
	}
	if len(initial.Plans) == 0 {
		t.Fatalf("test setup expected initial secondary plans")
	}
	beforePlans := simPlanSummary(initial.Plans)

	latest := MPTVersion{
		Height: 2,
		RootID: "root-2",
		Paths: []partitioning.StatePath{
			simStatePathWithChildCounts("Z", []string{"z1", "z2", "z3"}, []int{1, 1, 0}),
		},
	}
	updated, err := AddLatestMPTByKeyPartition(initial, latest, 3, partitioning.SortByKey, cfg)
	if err != nil {
		t.Fatalf("update AddLatestMPTByKeyPartition: %v", err)
	}
	if got := simPlanSummary(updated.Plans[:len(initial.Plans)]); got != beforePlans {
		t.Fatalf("old secondary plans were rewritten:\n got: %s\nwant: %s", got, beforePlans)
	}
	for _, plan := range updated.Plans[len(initial.Plans):] {
		if string(plan.SuperNode.PathKey) != "Z" {
			t.Fatalf("incremental update should only add latest-version SuperNode plans, got path=%s", plan.SuperNode.PathKey)
		}
	}
	if after := simPlanSummary(initial.Plans); after != beforePlans {
		t.Fatalf("oldState plans mutated:\n got: %s\nwant: %s", after, beforePlans)
	}
}

func TestKeyPartitionDeterministic(t *testing.T) {
	path := simStatePath("Z", "x", "b", "c")
	first, err := AssignNewPathByKey(path, 3, partitioning.SortByKey)
	if err != nil {
		t.Fatalf("AssignNewPathByKey: %v", err)
	}
	for i := 0; i < 20; i++ {
		got, err := AssignNewPathByKey(path, 3, partitioning.SortByKey)
		if err != nil {
			t.Fatalf("AssignNewPathByKey repeat %d: %v", i, err)
		}
		if got != first {
			t.Fatalf("non-deterministic assignment: first=%d got=%d", first, got)
		}
		if got < 0 || got >= 3 {
			t.Fatalf("partition out of range: %d", got)
		}
	}
}

func TestNoAffinityLogicUsed(t *testing.T) {
	oldPath := simStatePath("A", "old-0", "shared-1", "shared-2", "shared-3", "shared-4")
	oldState := &MPTForestState{
		Versions:     []MPTVersion{{Height: 1, RootID: "root-1", Paths: []partitioning.StatePath{oldPath}}},
		LatestHeight: 1,
		Partitions: []partitioning.Partition{
			{ID: 0, Paths: []partitioning.StatePath{oldPath}},
			{ID: 1},
			{ID: 2},
		},
	}
	newPath := simStatePath("Y", "new-0", "shared-1", "shared-2", "shared-3", "shared-4")
	selected, err := AssignNewPathByKey(newPath, 3, partitioning.SortByKey)
	if err != nil {
		t.Fatalf("AssignNewPathByKey: %v", err)
	}
	if selected != 1 {
		t.Fatalf("test setup expected key Y to map to partition-1, got %d", selected)
	}
	state, err := AddLatestMPTByKeyPartition(oldState, MPTVersion{Height: 2, RootID: "root-2", Paths: []partitioning.StatePath{newPath}}, 3, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("AddLatestMPTByKeyPartition: %v", err)
	}
	if partitionHasPath(state.Partitions[0], "Y") {
		t.Fatalf("new path should not inherit partition-0 despite many reused nodes")
	}
	if !partitionHasPath(state.Partitions[1], "Y") {
		t.Fatalf("new path should be assigned to key-selected partition-1")
	}
}

func TestPreserveLatestDoesNotChangePartitionAssignment(t *testing.T) {
	oldPath := simStatePathWithChildCounts("A", []string{"n1", "n2", "n3"}, []int{1, 1, 0})
	first := MPTVersion{Height: 1, RootID: "root-1", Paths: []partitioning.StatePath{oldPath}}
	state, err := AddLatestMPTByKeyPartition(nil, first, 3, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("initial AddLatestMPTByKeyPartition: %v", err)
	}
	latestPath := simStatePathWithChildCounts("Z", []string{"n8", "n9"}, []int{1, 0})
	want, err := AssignNewPathByKey(latestPath, 3, partitioning.SortByKey)
	if err != nil {
		t.Fatalf("AssignNewPathByKey: %v", err)
	}
	updated, err := AddLatestMPTByKeyPartition(state, MPTVersion{Height: 2, RootID: "root-2", Paths: []partitioning.StatePath{latestPath}}, 3, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("update AddLatestMPTByKeyPartition: %v", err)
	}
	for i := range updated.Partitions {
		has := partitionHasPath(updated.Partitions[i], "Z")
		if i == want && !has {
			t.Fatalf("latest path missing from key-selected partition-%d", want)
		}
		if i != want && has {
			t.Fatalf("PreserveLatest changed primary assignment: Z found in partition-%d, want only partition-%d", i, want)
		}
		if got := rawCount(updated.LocalViews[i], "Z"); got != len(latestPath.Nodes) {
			t.Fatalf("partition-%d latest raw nodes = %d, want %d", i, got, len(latestPath.Nodes))
		}
	}
}

func TestSimulationDoesNotRewriteSuperNodePlans(t *testing.T) {
	paths := []partitioning.StatePath{
		simStatePathWithChildCounts("A", []string{"n1", "n2", "n3", "n4"}, []int{2, 2, 1, 0}),
		simStatePathWithChildCounts("B", []string{"n1", "n2", "n5"}, []int{2, 2, 0}),
		simStatePathWithChildCounts("C", []string{"n1", "n6", "n7"}, []int{2, 1, 0}),
	}
	cfg := SimulationConfig{
		PartitionCount: 3,
		SortBy:         partitioning.SortByKey,
		Optimization:   simOptimizationConfig(),
		LocalView:      simulationCompressOptions(t),
	}
	partitions, err := partitioning.BuildPartitions(paths, cfg.PartitionCount, cfg.SortBy)
	if err != nil {
		t.Fatalf("BuildPartitions: %v", err)
	}
	expectedPlans, _, _, err := partitioning.OptimizeSuperNodeDistribution(paths, partitions, cfg.Optimization)
	if err != nil {
		t.Fatalf("OptimizeSuperNodeDistribution: %v", err)
	}
	result, err := RunPartitionedMPTSimulation(paths, cfg)
	if err != nil {
		t.Fatalf("RunPartitionedMPTSimulation: %v", err)
	}
	if got, want := simPlanSummary(result.Plans), simPlanSummary(expectedPlans); got != want {
		t.Fatalf("simulation rewrote partitioning plans:\n got: %s\nwant: %s", got, want)
	}
	for _, plan := range result.Plans {
		primary, ok := partitioning.LocatePartitionForPath(result.Partitions, plan.SuperNode.PathKey)
		if !ok {
			t.Fatalf("missing primary partition for plan path=%s", plan.SuperNode.PathKey)
		}
		if plan.AssignedPartition == primary {
			t.Fatalf("secondary SuperNode assigned back to primary owner: %+v", plan)
		}
	}
}

func TestSimulationDelegatesLocalViewToMptagg(t *testing.T) {
	paths := []partitioning.StatePath{
		simStatePathWithChildCounts("A", []string{"n1", "n2", "n3", "n4"}, []int{2, 2, 1, 0}),
		simStatePathWithChildCounts("B", []string{"n1", "n2", "n5"}, []int{2, 2, 0}),
	}
	cfg := SimulationConfig{
		PartitionCount: 2,
		SortBy:         partitioning.SortByKey,
		Optimization:   simOptimizationConfig(),
		LocalView:      simulationCompressOptions(t),
	}
	result, err := RunPartitionedMPTSimulation(paths, cfg)
	if err != nil {
		t.Fatalf("RunPartitionedMPTSimulation: %v", err)
	}
	expected, err := mptagg.BuildLocalPartitionViewFromRawNodeSet(result.Paths, result.RawNodeSets[1], 1, cfg.LocalView)
	if err != nil {
		t.Fatalf("BuildLocalPartitionViewFromRawNodeSet: %v", err)
	}
	gotSummary := simLocalViewSummary(result.LocalViews[1])
	wantSummary := simLocalViewSummary(expected)
	if gotSummary != wantSummary {
		t.Fatalf("simulation local view differs from direct mptagg output:\n got: %s\nwant: %s", gotSummary, wantSummary)
	}
}

func TestPartitionedMPTSimDelegatesAggToMptagg(t *testing.T) {
	paths := []partitioning.StatePath{
		simStatePathWithChildCounts("A", []string{"n1", "n2", "n3"}, []int{2, 2, 0}),
		simStatePathWithChildCounts("B", []string{"n1", "n2", "n5"}, []int{2, 2, 0}),
	}
	cfg := SimulationConfig{
		PartitionCount: 2,
		SortBy:         partitioning.SortByKey,
		Optimization:   simOptimizationConfig(),
		LocalView:      simulationCompressOptions(t),
	}
	cfg.LocalView.PreserveLatest = false
	cfg.LocalView.PreserveLatestSet = true
	result, err := RunPartitionedMPTSimulation(paths, cfg)
	if err != nil {
		t.Fatalf("RunPartitionedMPTSimulation: %v", err)
	}
	direct, err := mptagg.BuildLocalPartitionViewFromRawNodeSet(result.Paths, result.RawNodeSets[1], 1, cfg.LocalView)
	if err != nil {
		t.Fatalf("BuildLocalPartitionViewFromRawNodeSet: %v", err)
	}
	got := result.LocalViews[1]
	if simLocalViewSummary(got) != simLocalViewSummary(direct) {
		t.Fatalf("partitionedmptsim must delegate local agg view to mptagg")
	}
	if got.RawAvailableHashes == nil {
		t.Fatalf("mptagg local view should expose rawAvailableHashes")
	}
	if got.CandidateAggregatedNodes == nil || got.Edges == nil {
		t.Fatalf("mptagg should populate candidates and replacement edges")
	}
}

func TestNoSecondarySuperNodeStoredOnPrimaryOwner(t *testing.T) {
	pathA := simStatePathWithChildCounts("A", []string{"n1", "n2", "n3", "n4"}, []int{1, 1, 1, 0})
	paths := []partitioning.StatePath{pathA}
	partitions := []partitioning.Partition{
		{ID: 0, Paths: []partitioning.StatePath{pathA}},
		{ID: 1},
	}
	cfg := simOptimizationConfig()
	cfg.HighRiskMultiplier = 0.9
	plans, _, superNodes, err := partitioning.OptimizeSuperNodeDistribution(paths, partitions, cfg)
	if err != nil {
		t.Fatalf("OptimizeSuperNodeDistribution: %v", err)
	}
	if len(superNodes) == 0 || len(plans) == 0 {
		t.Fatalf("expected SuperNode and non-primary secondary plan, superNodes=%d plans=%d", len(superNodes), len(plans))
	}
	for _, plan := range plans {
		if plan.AssignedPartition == 0 {
			t.Fatalf("partitioning assigned secondary SuperNode back to primary owner: %+v", plan)
		}
	}
	rawNodeSets := partitioning.BuildRawNodeSets(partitions, plans, partitioning.BuildLatestReachableNodeHashSet(paths), false)
	view, err := mptagg.BuildLocalPartitionViewFromRawNodeSet(paths, rawNodeSets[0], 0, simulationCompressOptions(t))
	if err != nil {
		t.Fatalf("BuildLocalPartitionViewFromRawNodeSet: %v", err)
	}
	if len(view.RawSuperNodes) != 0 {
		t.Fatalf("mptagg raw-set mainline should not receive SuperNodes: %+v", view.RawSuperNodes)
	}
}

func TestSharedNodeStillIncludedInMultipleCommitments(t *testing.T) {
	oldPath := simStatePath("A", "a", "b", "c")
	oldState, err := AddLatestMPTByKeyPartition(nil, MPTVersion{Height: 1, RootID: "root-1", Paths: []partitioning.StatePath{oldPath}}, 4, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("initial AddLatestMPTByKeyPartition: %v", err)
	}
	newPath := simStatePath("Z", "x", "b", "c")
	stateWithSharedLatest, err := AddLatestMPTByKeyPartition(oldState, MPTVersion{Height: 2, RootID: "root-2", Paths: []partitioning.StatePath{newPath}}, 4, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("incremental AddLatestMPTByKeyPartition: %v", err)
	}
	latestPath := simStatePath("Y", "y1", "y2", "y3")
	state, err := AddLatestMPTByKeyPartition(stateWithSharedLatest, MPTVersion{Height: 3, RootID: "root-3", Paths: []partitioning.StatePath{latestPath}}, 4, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("second incremental AddLatestMPTByKeyPartition: %v", err)
	}

	var view *mptagg.LocalPartitionView
	var oldAgg *mptagg.AggregatedNode
	var newAgg *mptagg.AggregatedNode
	for id := 0; id < len(state.Partitions); id++ {
		candidate := state.LocalViews[id]
		if candidate == nil {
			continue
		}
		oldAgg = findSimAggregated(candidate, "A")
		newAgg = findSimAggregated(candidate, "Z")
		if oldAgg != nil && newAgg != nil {
			view = candidate
			break
		}
	}
	if oldAgg == nil || newAgg == nil {
		t.Fatalf("expected at least one local view to compress both historical shared-node paths; found view=%v old=%v new=%v", view != nil, oldAgg != nil, newAgg != nil)
	}
	if !aggContainsHash(oldAgg, "b") || !aggContainsHash(oldAgg, "c") {
		t.Fatalf("oldPath aggregated node should include shared b/c hashes")
	}
	if !aggContainsHash(newAgg, "b") || !aggContainsHash(newAgg, "c") {
		t.Fatalf("newPath aggregated node should include shared b/c hashes")
	}
	if oldAgg == newAgg {
		t.Fatalf("expected two logical AggregatedNode instances")
	}
	if oldAgg.Commitment.Equal(&newAgg.Commitment) {
		t.Fatalf("[a,b,c] and [x,b,c] should have different path-contextual commitments")
	}
}

func TestSimulationUsesMptaggLatestBoundaryCompression(t *testing.T) {
	oldPath := simStatePath("A", "n1", "n2", "n3")
	oldState, err := AddLatestMPTByKeyPartition(nil, MPTVersion{Height: 1, RootID: "root-1", Paths: []partitioning.StatePath{oldPath}}, 3, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("initial AddLatestMPTByKeyPartition: %v", err)
	}
	latestPath := simStatePath("Z", "m1", "n2", "n3")
	latest := MPTVersion{Height: 2, RootID: "root-2", Paths: []partitioning.StatePath{latestPath}}
	state, err := AddLatestMPTByKeyPartition(oldState, latest, 3, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("incremental AddLatestMPTByKeyPartition: %v", err)
	}

	reachable := BuildLatestReachableSet(latest)
	if !reachable[hex.EncodeToString([]byte("n2"))] || !reachable[hex.EncodeToString([]byte("n3"))] {
		t.Fatalf("latest reachable set should contain reused n2/n3: %v", reachable)
	}
	view := state.LocalViews[2]
	if view == nil {
		t.Fatalf("missing partition-2 local view")
	}
	oldAgg := findSimAggregated(view, "A")
	if oldAgg == nil {
		t.Fatalf("old non-primary path should be compressed as one full-path commitment")
	}
	if len(oldAgg.OriginalNodeHashes) != 3 || !bytes.Equal(oldAgg.OriginalNodeHashes[0], []byte("n1")) || !bytes.Equal(oldAgg.OriginalNodeHashes[1], []byte("n2")) || !bytes.Equal(oldAgg.OriginalNodeHashes[2], []byte("n3")) {
		t.Fatalf("old aggregated hashes = %s, want n1->n2->n3", simBytesHashes(oldAgg.OriginalNodeHashes))
	}
	if !aggContainsHash(oldAgg, "n2") || !aggContainsHash(oldAgg, "n3") {
		t.Fatalf("full-path commitment should include reused latest-reachable n2/n3")
	}
	if got := rawCount(view, "Z"); got != len(latestPath.Nodes) {
		t.Fatalf("latest path should be preserved raw in every local view, got %d raw nodes", got)
	}
}

func TestBuildLatestReachableSetFromLatestRoot(t *testing.T) {
	root1, err := mpttest.BuildOrUpdateTestMPT(nil, initialMPTEntries())
	if err != nil {
		t.Fatalf("build root-1: %v", err)
	}
	root2, err := mpttest.BuildOrUpdateTestMPT(root1, updateMPTEntries())
	if err != nil {
		t.Fatalf("update root-2: %v", err)
	}
	root1Paths := mpttest.ExtractStatePathsFromTestMPT(t, root1)
	root2Paths := mpttest.ExtractStatePathsFromTestMPT(t, root2)
	assertPathLabels(t, root2Paths, []string{"A", "B", "C", "Y", "Z"})
	latest := MPTVersion{Height: 2, RootID: root2.Root.String(), Paths: root2Paths}
	reachable := BuildLatestReachableSet(latest)
	if len(reachable) <= len(updateMPTEntries()) {
		t.Fatalf("R_latest too small; got %d hashes for full latest root", len(reachable))
	}
	for _, path := range root2Paths {
		for _, node := range path.Nodes {
			if !reachable[hex.EncodeToString(node.Hash)] {
				t.Fatalf("R_latest missing root-2 node key=0x%x hash=%x", path.Key, node.Hash)
			}
		}
	}
	shared := mpttest.SharedNodeHashes(root1Paths, root2Paths)
	if len(shared) == 0 {
		t.Fatalf("test setup expected reused old nodes")
	}
	for _, hash := range shared {
		if !reachable[hex.EncodeToString(hash)] {
			t.Fatalf("R_latest missing reused latest-reachable old node %x", hash)
		}
	}
}

func TestMultiVersionMPTUpdatePartitionAndCompressedViewVerbose(t *testing.T) {
	partitionCount := 4

	// 过程 1：用项目已有 trie/MPT update 语义从空树构造 root-1。
	// 这里不手写 fake path，也不直接构造测试用 mini trie。
	initialEntries := []mpttest.TestTrieEntry{
		{Name: "A", Key: []byte{0x12, 0x34}, Value: simLongValue("A-v1")},
		{Name: "B", Key: []byte{0x12, 0x56}, Value: simLongValue("B-v1")},
		{Name: "C", Key: []byte{0x16, 0xab}, Value: simLongValue("C-v1")},
	}
	firstTree, err := mpttest.BuildOrUpdateTestMPT(nil, initialEntries)
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT root-1: %v", err)
	}
	firstPaths := mpttest.ExtractStatePathsFromTestMPT(t, firstTree)
	first := MPTVersion{
		Height: 1,
		RootID: firstTree.Root.String(),
		Paths:  firstPaths,
	}

	// 过程 2：把 root-1 的完整 StatePaths 交给 partitioning。
	// AddLatestMPTByKeyPartition 内部先按 key 做 primary partition，
	// 再调用 OptimizeSuperNodeDistribution 做 SuperNode 二次分配，
	// 最后调用 mptagg.BuildLocalPartitionView 构造每个 partition 的本地压缩视图。
	initial, err := AddLatestMPTByKeyPartition(nil, first, partitionCount, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("initial AddLatestMPTByKeyPartition: %v", err)
	}
	if len(initial.Partitions) != partitionCount {
		t.Fatalf("initial partition count = %d, want %d", len(initial.Partitions), partitionCount)
	}
	t.Logf("root-1 initial entries:")
	logTestEntries(t, initialEntries)
	t.Logf("root-1 full state paths:")
	for _, path := range first.Paths {
		t.Logf("  %s key=0x%x nibbles=%s nodes=%s", mpttest.LabelForPath(path), path.Key, mpttest.FormatNibbles(path.PathNibbles), simShortPathHashes(path))
	}
	t.Logf("root-1 trie structure:\n%s", mpttest.FormatTrie(firstTree))
	logSimForestSummary(t, "initial forest", initial)

	// 过程 3：在 root-1 基础上调用真实 trie/MPT update，生成 root-2。
	// root-2 必须包含 A/B/C/Y/Z 的完整最新状态，而不是只包含本轮更新的 Y/Z。
	updateEntries := []mpttest.TestTrieEntry{
		{Name: "Z", Key: []byte{0x12, 0x88}, Value: simLongValue("Z-v2")},
		{Name: "Y", Key: []byte{0x18, 0xff}, Value: simLongValue("Y-v2")},
	}
	latestTree, err := mpttest.BuildOrUpdateTestMPT(firstTree, updateEntries)
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT root-2: %v", err)
	}
	latestPaths := mpttest.ExtractStatePathsFromTestMPT(t, latestTree)
	assertPathLabels(t, latestPaths, []string{"A", "B", "C", "Y", "Z"})
	if len(latestPaths) == len(updateEntries) {
		t.Fatalf("root-2 has only update entries; want complete latest state")
	}
	shared := mpttest.SharedNodeHashes(firstPaths, latestPaths)
	if len(shared) == 0 {
		t.Fatalf("root-1/root-2 share no node hashes")
	}
	latest := MPTVersion{
		Height: 2,
		RootID: latestTree.Root.String(),
		Paths:  latestPaths,
	}
	t.Logf("root-2 update entries:")
	logTestEntries(t, updateEntries)
	t.Logf("root-2 is generated by updating root-1")
	t.Logf("root-2 full state paths:")
	for _, path := range latest.Paths {
		// 过程 4：仅用于 verbose 展示每条最新 path 会按 key 落到哪个 primary partition。
		// 真正的分区结果仍由下面的 AddLatestMPTByKeyPartition 统一生成。
		selected, err := AssignNewPathByKey(path, partitionCount, partitioning.SortByKey)
		if err != nil {
			t.Fatalf("AssignNewPathByKey %x: %v", path.Key, err)
		}
		t.Logf("  %s key=0x%x nibbles=%s path=%s assignedByKey=partition-%d", mpttest.LabelForPath(path), path.Key, mpttest.FormatNibbles(path.PathNibbles), simShortPathHashes(path), selected)
	}
	t.Logf("root-2 trie structure:\n%s", mpttest.FormatTrie(latestTree))
	t.Logf("shared node hashes between root-1 and root-2:")
	for _, hash := range shared {
		t.Logf("  %s", simShortHashDisplay(hash))
	}

	// 过程 5：把 root-2 作为新 latest 加入多版本 forest。
	// 这里保留 root-1 的历史 StatePaths，同时把 root-2 的完整 StatePaths 加入系统；
	// partitioning 只给新增 latest paths 追加 primary/secondary 分配，
	// partitioning 把 primary-owned、assigned-supernode、R_latest 摊平成 RawNodeSets；
	// mptagg 只基于当前 partition 的 rawSet 构造本地 RAW/AGG/NEXT 视图。
	updated, err := AddLatestMPTByKeyPartition(initial, latest, partitionCount, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("update AddLatestMPTByKeyPartition: %v", err)
	}
	if len(updated.Partitions) != partitionCount {
		t.Fatalf("updated partition count = %d, want %d", len(updated.Partitions), partitionCount)
	}

	// 过程 6：从完整 latest root(root-2) 的 StatePaths 构造 R_latest。
	// mptagg 的 preserve-latest/latest-boundary 只按 node hash 是否属于 R_latest 判断。
	logSimLatestReachable(t, latest)

	// 过程 7：打印两轮 partition 结果和 SuperNode 二次分配结果。
	logSimForestSummary(t, "updated forest", updated)

	// 过程 8：打印 mptagg 生成的最终本地压缩 MPT。
	// 这里可以看到 RAW 节点、AGG 压缩段、NEXT boundary，以及 hash-reuse 的来源。
	logSimFinalLocalCompressedTrees(t, updated)

	for _, view := range updated.LocalViews {
		if view == nil {
			t.Fatalf("missing local view")
		}
	}
}

func TestRootAwareManagerPartitionAndAggVerbose(t *testing.T) {
	partitionCount := 4
	rootsSeen := make(map[string]bool)
	manager := &partitioning.MPTPartitionManager{
		PartitionCount:    partitionCount,
		SortBy:            partitioning.SortByKey,
		Config:            simOptimizationConfig(),
		PreserveLatest:    false,
		PreserveLatestSet: true,
	}
	opts := simulationCompressOptions(t)
	owned := rootAwareOwnedHandles(partitionCount)

	root1, err := mpttest.BuildOrUpdateTestMPT(nil, initialMPTEntries())
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT root1: %v", err)
	}
	result1, err := manager.AddLatestRoot(root1)
	if err != nil {
		t.Fatalf("AddLatestRoot root1: %v", err)
	}
	recordRootHash(t, rootsSeen, "root1", result1.RootHash)
	logRootAwarePartitionAndAgg(t, "root1", root1, result1, owned, opts)

	root2, err := mpttest.BuildOrUpdateTestMPT(root1, updateMPTEntries())
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT root2: %v", err)
	}
	result2, err := manager.AddLatestRoot(root2)
	if err != nil {
		t.Fatalf("AddLatestRoot root2: %v", err)
	}
	recordRootHash(t, rootsSeen, "root2", result2.RootHash)
	logRootAwarePartitionAndAgg(t, "root2", root2, result2, owned, opts)

	assertPathLabels(t, result2.LatestPaths, []string{"A", "B", "C", "Y", "Z"})
	if result2.PreviousRootHash != result1.RootHash {
		t.Fatalf("previous root hash = %s, want %s", result2.PreviousRootHash, result1.RootHash)
	}
	if len(rootsSeen) != 2 {
		t.Fatalf("rootsSeen size = %d, want 2", len(rootsSeen))
	}
	if len(result2.Diff.Changed) == 0 {
		t.Fatalf("root2 diff should include changed paths")
	}
	if len(result2.RawNodeSets) != partitionCount {
		t.Fatalf("root2 RawNodeSets = %d, want %d", len(result2.RawNodeSets), partitionCount)
	}
	nonEmptyRawSets := 0
	for i := range result2.RawNodeSets {
		if len(result2.RawNodeSets[i]) > 0 {
			nonEmptyRawSets++
		}
	}
	if nonEmptyRawSets == 0 {
		t.Fatalf("root2 RawNodeSets are all empty")
	}
}

func TestBuildOrUpdateMPTInitialAndUpdate(t *testing.T) {
	root1, err := mpttest.BuildOrUpdateTestMPT(nil, initialMPTEntries())
	if err != nil {
		t.Fatalf("build root-1: %v", err)
	}
	root1Paths := mpttest.ExtractStatePathsFromTestMPT(t, root1)
	assertPathLabels(t, root1Paths, []string{"A", "B", "C"})

	root2, err := mpttest.BuildOrUpdateTestMPT(root1, updateMPTEntries())
	if err != nil {
		t.Fatalf("update root-2: %v", err)
	}
	root2Paths := mpttest.ExtractStatePathsFromTestMPT(t, root2)
	assertPathLabels(t, root2Paths, []string{"A", "B", "C", "Y", "Z"})
	if len(root2Paths) == len(updateMPTEntries()) {
		t.Fatalf("root-2 was built only from update entries")
	}
	if root1.Root == root2.Root {
		t.Fatalf("root hash did not change after updates")
	}
}

func TestUpdatedMPTReusesUnchangedNodes(t *testing.T) {
	root1, err := mpttest.BuildOrUpdateTestMPT(nil, initialMPTEntries())
	if err != nil {
		t.Fatalf("build root-1: %v", err)
	}
	root2, err := mpttest.BuildOrUpdateTestMPT(root1, updateMPTEntries())
	if err != nil {
		t.Fatalf("update root-2: %v", err)
	}
	root1Paths := mpttest.ExtractStatePathsFromTestMPT(t, root1)
	root2Paths := mpttest.ExtractStatePathsFromTestMPT(t, root2)
	shared := mpttest.SharedNodeHashes(root1Paths, root2Paths)
	if len(shared) == 0 {
		t.Fatalf("root-1 and root-2 should reuse unchanged trie nodes")
	}
	t.Logf("shared node hashes=%s", simBytesHashes(shared))
}

func TestExtractStatePathsUseRealTrieNibbles(t *testing.T) {
	tree, err := mpttest.BuildOrUpdateTestMPT(nil, initialMPTEntries())
	if err != nil {
		t.Fatalf("build root: %v", err)
	}
	paths := mpttest.ExtractStatePathsFromTestMPT(t, tree)
	pathA, ok := mpttest.FindPathByName(paths, "A")
	if !ok {
		t.Fatalf("missing path A")
	}
	if !bytes.Equal(pathA.PathNibbles, []byte{1, 2, 3, 4}) {
		t.Fatalf("A PathNibbles = %x, want 1234", pathA.PathNibbles)
	}
	for _, path := range paths {
		if !bytes.Equal(path.PathNibbles, mpttest.BytesToNibbles(path.Key)) {
			t.Fatalf("%s PathNibbles = %x key=0x%x", mpttest.LabelForPath(path), path.PathNibbles, path.Key)
		}
	}
}

func TestVerboseDoesNotUseUnknownPathLabels(t *testing.T) {
	root1, err := mpttest.BuildOrUpdateTestMPT(nil, initialMPTEntries())
	if err != nil {
		t.Fatalf("build root-1: %v", err)
	}
	root2, err := mpttest.BuildOrUpdateTestMPT(root1, updateMPTEntries())
	if err != nil {
		t.Fatalf("update root-2: %v", err)
	}
	rendered := mpttest.FormatTrie(root2)
	for _, bad := range []string{"path=?", "path=4", "path=V"} {
		if strings.Contains(rendered, bad) {
			t.Fatalf("verbose output contains %s:\n%s", bad, rendered)
		}
	}
	for _, label := range []string{"A", "B", "C", "Y", "Z"} {
		if !strings.Contains(rendered, label+" key=") {
			t.Fatalf("verbose output missing label %s:\n%s", label, rendered)
		}
	}
}

func TestVerboseDoesNotMislabelOldRootAsPreserveLatest(t *testing.T) {
	root1, err := mpttest.BuildOrUpdateTestMPT(nil, initialMPTEntries())
	if err != nil {
		t.Fatalf("build root-1: %v", err)
	}
	first := MPTVersion{Height: 1, RootID: root1.Root.String(), Paths: mpttest.ExtractStatePathsFromTestMPT(t, root1)}
	initial, err := AddLatestMPTByKeyPartition(nil, first, 3, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("initial AddLatestMPTByKeyPartition: %v", err)
	}
	root2, err := mpttest.BuildOrUpdateTestMPT(root1, updateMPTEntries())
	if err != nil {
		t.Fatalf("update root-2: %v", err)
	}
	latest := MPTVersion{Height: 2, RootID: root2.Root.String(), Paths: mpttest.ExtractStatePathsFromTestMPT(t, root2)}
	updated, err := AddLatestMPTByKeyPartition(initial, latest, 3, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("update AddLatestMPTByKeyPartition: %v", err)
	}
	reachable := BuildLatestReachableSet(latest)
	oldRootHash := root1.Root.Bytes()
	if reachable[hex.EncodeToString(oldRootHash)] {
		t.Fatalf("test setup expected root-1 root hash to be historical-only")
	}
	for _, view := range updated.LocalViews {
		for _, node := range view.Nodes {
			if node.Kind != mptagg.RawMPTNode || node.RawReason != "preserve-latest" {
				continue
			}
			hashKey := hex.EncodeToString(node.RawNode.Hash)
			if !reachable[hashKey] {
				t.Fatalf("partition-%d mislabeled non-latest hash %s path=0x%x pos=%d as preserve-latest", view.PartitionID, hashKey, node.PathKey, node.RawNode.Position)
			}
			if bytes.Equal(node.RawNode.Hash, oldRootHash) {
				t.Fatalf("old root hash %x was mislabeled preserve-latest", oldRootHash)
			}
		}
	}
}

func TestExtractStatePathsFromTrie(t *testing.T) {
	tree := mpttest.BuildTestMPTFromEntries(t, []mpttest.TestTrieEntry{
		{Name: "A", Key: []byte{0x12, 0x34}, Value: []byte("value-A")},
		{Name: "B", Key: []byte{0x12, 0x56}, Value: []byte("value-B")},
		{Name: "C", Key: []byte{0x16, 0xab}, Value: []byte("value-C")},
	})
	paths := mpttest.ExtractStatePathsFromTestMPT(t, tree)
	want := map[string][]byte{
		"A": {1, 2, 3, 4},
		"B": {1, 2, 5, 6},
		"C": {1, 6, 0xa, 0xb},
	}
	for name, nibbles := range want {
		path, ok := mpttest.FindPathByName(paths, name)
		if !ok {
			t.Fatalf("missing path %s", name)
		}
		if !bytes.Equal(path.PathNibbles, nibbles) {
			t.Fatalf("%s PathNibbles = %x, want %x", name, path.PathNibbles, nibbles)
		}
		for i, node := range path.Nodes {
			if node.Raw == nil || node.NodeType == "" {
				t.Fatalf("%s node %d was not extracted from a real test trie node: %+v", name, i, node)
			}
			wantParent := i - 1
			if i == 0 {
				wantParent = -1
			}
			if node.ParentPosition != wantParent {
				t.Fatalf("%s node %d parent=%d want %d", name, i, node.ParentPosition, wantParent)
			}
			if node.ChildCount < 0 {
				t.Fatalf("%s node %d negative ChildCount", name, i)
			}
		}
	}
}

func TestNoSyntheticPathNibbles(t *testing.T) {
	banned := []string{"synthesizePathNibbles", "mockPathNibblesFromPosition", "positionToNibble", "fakeNibblesByIndex"}
	source, err := os.ReadFile("simulation_test.go")
	if err != nil {
		t.Fatalf("read simulation_test.go: %v", err)
	}
	start := bytes.Index(source, []byte("func TestMultiVersionMPTUpdatePartitionAndCompressedViewVerbose"))
	end := bytes.Index(source, []byte("func TestBuildOrUpdateMPTInitialAndUpdate"))
	if start < 0 || end <= start {
		t.Fatalf("could not isolate verbose test source")
	}
	verboseSource := source[start:end]
	for _, name := range banned {
		if bytes.Contains(verboseSource, []byte(name)) {
			t.Fatalf("verbose test still references synthetic nibble helper %s", name)
		}
	}
	tree := mpttest.BuildTestMPTFromEntries(t, []mpttest.TestTrieEntry{{Name: "A", Key: []byte{0x12, 0x34}, Value: []byte("v")}})
	paths := mpttest.ExtractStatePathsFromTestMPT(t, tree)
	if len(paths) != 1 || !bytes.Equal(paths[0].PathNibbles, mpttest.BytesToNibbles(paths[0].Key)) {
		t.Fatalf("PathNibbles must come from key nibbles, got %x key=0x%x", paths[0].PathNibbles, paths[0].Key)
	}
}

func TestPartitionedMPTSimHashReuseVerbose(t *testing.T) {
	first := MPTVersion{
		Height: 1,
		RootID: "root-1",
		Paths: []partitioning.StatePath{
			simStatePathWithChildCounts("A", []string{"n1", "n2", "n3", "n4"}, []int{2, 2, 1, 0}),
			simStatePathWithChildCounts("B", []string{"n1", "n2", "n5"}, []int{2, 2, 0}),
			simStatePathWithChildCounts("C", []string{"n1", "n6", "n7"}, []int{2, 1, 0}),
		},
	}
	state, err := AddLatestMPTByKeyPartition(nil, first, 3, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("AddLatestMPTByKeyPartition: %v", err)
	}
	latest := MPTVersion{
		Height: 2,
		RootID: "root-2",
		Paths: []partitioning.StatePath{
			simStatePathWithChildCounts("Z", []string{"n8", "n3", "n4"}, []int{1, 1, 0}),
			simStatePathWithChildCounts("Y", []string{"n8", "n9"}, []int{1, 1}),
		},
	}
	state, err = AddLatestMPTByKeyPartition(state, latest, 3, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("update AddLatestMPTByKeyPartition: %v", err)
	}
	view := state.LocalViews[1]
	if view == nil {
		t.Fatalf("missing partition-1 local view")
	}
	t.Logf("partition-1 owns n1/n2 from primary B and n3/n4 from assigned SuperNode, so path A is locally complete")
	for _, node := range view.Nodes {
		if node.Kind == mptagg.RawMPTNode && string(node.PathKey) == "A" {
			t.Logf("raw path=A pos=%d hash=%s reason=%s", node.RawNode.Position, node.RawNode.Hash, node.RawReason)
		}
	}
	if got := rawCount(view, "A"); got != 4 {
		t.Fatalf("complete local path A should be emitted raw, got %d raw nodes", got)
	}
	if agg := findSimAggregated(view, "A"); agg != nil {
		t.Fatalf("complete local path A should not also create a commitment")
	}
}

func TestMPTForestKeyPartitionUpdateVerbose(t *testing.T) {
	oldPath := simStatePath("A", "a", "b", "c")
	oldState, err := AddLatestMPTByKeyPartition(nil, MPTVersion{Height: 1, RootID: "root-1", Paths: []partitioning.StatePath{oldPath}}, 3, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("initial AddLatestMPTByKeyPartition: %v", err)
	}
	newPath := simStatePath("Z", "x", "b", "c")
	selected, err := AssignNewPathByKey(newPath, 3, partitioning.SortByKey)
	if err != nil {
		t.Fatalf("AssignNewPathByKey: %v", err)
	}
	state, err := AddLatestMPTByKeyPartition(oldState, MPTVersion{Height: 2, RootID: "root-2", Paths: []partitioning.StatePath{newPath}}, 3, partitioning.SortByKey, simOptimizationConfig())
	if err != nil {
		t.Fatalf("incremental AddLatestMPTByKeyPartition: %v", err)
	}

	t.Logf("oldState versions=%s latest=%d", simVersionSummary(oldState.Versions), oldState.LatestHeight)
	t.Logf("old partitions=%s", simPartitionsSummary(oldState.Partitions))
	t.Logf("new latest MPT paths=%s", simPathHashes(newPath))
	t.Logf("newPath key=%s key-based partition=%d", newPath.Key, selected)
	t.Logf("newPath reuses old node hashes b/c, but reused old node does not affect partition assignment")
	t.Logf("updated partitions=%s", simPartitionsSummary(state.Partitions))
	for i, sn := range state.SuperNodes {
		t.Logf("SuperNode[%d] path=%s nodes=%s parentPartition=%d", i, sn.PathKey, simNodeHashes(sn.Nodes), sn.ParentPartition)
	}
	for i, plan := range state.Plans {
		t.Logf("plan[%d] path=%s assigned=%d reason=%s", i, plan.SuperNode.PathKey, plan.AssignedPartition, plan.Reason)
	}
	for id := 0; id < len(state.Partitions); id++ {
		view := state.LocalViews[id]
		if view == nil {
			t.Fatalf("missing LocalView for partition-%d", id)
		}
		t.Logf("local view partition-%d from mptagg: rawPaths=%d rawSuperNodes=%d aggregated=%d", id, len(view.RawPathKeys), len(view.RawSuperNodes), len(view.AggregatedNodes))
		for _, agg := range view.AggregatedNodes {
			t.Logf("  aggregated path=%s hashes=%s commitment=%s", agg.PathKey, simBytesHashes(agg.OriginalNodeHashes), simCommitmentString(agg))
		}
	}
	if len(state.Partitions) != len(oldState.Partitions) {
		t.Fatalf("partition count changed")
	}
	if !partitionHasPath(state.Partitions[0], "A") {
		t.Fatalf("oldPath was repartitioned")
	}
}

func TestPartitionedMPTSimulationEndToEnd(t *testing.T) {
	mpt := simulationMPT()
	result, err := RunMockMPTSimulation(mpt, SimulationConfig{
		PartitionCount: 2,
		SortBy:         partitioning.SortByKey,
		Optimization: partitioning.OptimizationConfig{
			Beta:               1.2,
			HighRiskMultiplier: 1.1,
			MaxSuperNodeLength: 3,
		},
		LocalView: simulationCompressOptions(t),
	})
	if err != nil {
		t.Fatalf("RunMockMPTSimulation: %v", err)
	}
	if len(result.Paths) != 3 {
		t.Fatalf("extracted StatePaths = %d, want 3", len(result.Paths))
	}
	if len(result.Partitions) != 2 {
		t.Fatalf("partition count = %d, want 2", len(result.Partitions))
	}
	if len(result.LocalViews) != len(result.Partitions) {
		t.Fatalf("local views = %d, want %d", len(result.LocalViews), len(result.Partitions))
	}
	if len(result.Plans) == 0 || len(result.SuperNodes) == 0 || len(result.HighRisk) == 0 {
		t.Fatalf("expected non-empty plans/superNodes/highRisk, got plans=%d superNodes=%d high=%d", len(result.Plans), len(result.SuperNodes), len(result.HighRisk))
	}
	for _, plan := range result.Plans {
		if plan.AssignedPartition < 0 || plan.AssignedPartition >= len(result.Partitions) {
			t.Fatalf("plan assigned outside existing partitions: %+v", plan)
		}
	}

	for _, view := range result.LocalViews {
		if view.PartitionID < 0 || view.PartitionID >= len(result.Partitions) {
			t.Fatalf("view partitionID outside existing partitions: %d", view.PartitionID)
		}
		for _, path := range result.Partitions[view.PartitionID].Paths {
			if got := rawCount(view, string(path.Key)); got != len(path.Nodes) {
				t.Fatalf("partition-%d owned path %s raw nodes = %d, want %d", view.PartitionID, path.Key, got, len(path.Nodes))
			}
		}
		for _, plan := range result.Plans {
			if plan.AssignedPartition != view.PartitionID {
				continue
			}
			path, ok := findStatePath(result.Paths, plan.SuperNode.PathKey)
			if !ok {
				t.Fatalf("missing StatePath for plan path=%s", plan.SuperNode.PathKey)
			}
			if rawCount(view, string(plan.SuperNode.PathKey)) == len(path.Nodes) {
				continue
			}
			agg := findSimAggregated(view, string(plan.SuperNode.PathKey))
			if agg == nil {
				t.Fatalf("partition-%d missing full-path commitment for incomplete assigned SuperNode path=%s", view.PartitionID, plan.SuperNode.PathKey)
			}
			for _, node := range plan.SuperNode.Nodes {
				if !hasAttachedRaw(agg, node.Position) {
					t.Fatalf("partition-%d missing attached SuperNode raw node path=%s pos=%d", view.PartitionID, plan.SuperNode.PathKey, node.Position)
				}
			}
		}
	}
}

func TestPartitionedMPTSimulationVerbose(t *testing.T) {
	mpt := simulationMPT()
	extracted, err := ExtractStatePathsFromMockMPT(mpt)
	if err != nil {
		t.Fatalf("ExtractStatePathsFromMockMPT: %v", err)
	}
	result, err := RunMockMPTSimulation(mpt, SimulationConfig{
		PartitionCount: 2,
		SortBy:         partitioning.SortByKey,
		Optimization: partitioning.OptimizationConfig{
			Beta:               1.2,
			HighRiskMultiplier: 1.1,
			MaxSuperNodeLength: 3,
		},
		LocalView: simulationCompressOptions(t),
	})
	if err != nil {
		t.Fatalf("RunMockMPTSimulation: %v", err)
	}

	t.Logf("simulation input full mock MPT:")
	t.Logf("  n1")
	t.Logf("  |-- n2")
	t.Logf("  |   |-- n3")
	t.Logf("  |   |   `-- n4 leaf account-A")
	t.Logf("  |   `-- n5 leaf account-B")
	t.Logf("  `-- n6")
	t.Logf("      `-- n7 leaf account-C")
	t.Logf("extracted StatePaths:")
	for _, path := range extracted {
		t.Logf("  path=%s nodes=%s", path.Key, simPathHashes(path))
	}
	t.Logf("first partition result:")
	for _, part := range result.Partitions {
		t.Logf("  partition-%d paths=%d", part.ID, len(part.Paths))
		for _, path := range part.Paths {
			t.Logf("    owned path=%s nodes=%s", path.Key, simPathHashes(path))
		}
	}
	t.Logf("SuperNode reallocation plans:")
	for i, plan := range result.Plans {
		t.Logf("  plan[%d] path=%s nodes=%s preferred=%d assigned=%d reason=%s",
			i, plan.SuperNode.PathKey, simNodeHashes(plan.SuperNode.Nodes), plan.PreferredPartition, plan.AssignedPartition, plan.Reason)
	}
	for _, view := range result.LocalViews {
		t.Logf("local view partition-%d:", view.PartitionID)
		for _, node := range view.Nodes {
			switch node.Kind {
			case mptagg.RawMPTNode:
				t.Logf("  raw path=%s pos=%d hash=%s reason=%s", node.PathKey, node.RawNode.Position, node.RawNode.Hash, node.RawReason)
			case mptagg.AggregatedCommitmentNode:
				agg := node.Aggregated
				t.Logf("  aggregated path=%s next=%d commitment=%s",
					agg.PathKey, agg.NextPosition, simCommitmentString(agg))
			}
		}
		t.Logf("  summary rawPaths=%d assignedSuperNodes=%d aggregated=%d", len(view.RawPathKeys), len(view.RawSuperNodes), len(view.AggregatedNodes))
	}
}

func TestExtractStatePathsFromMockMPT(t *testing.T) {
	paths, err := ExtractStatePathsFromMockMPT(simulationMPT())
	if err != nil {
		t.Fatalf("ExtractStatePathsFromMockMPT: %v", err)
	}
	if len(paths) != 3 {
		t.Fatalf("paths = %d, want 3", len(paths))
	}
	want := map[string]string{
		"account-A": "n1->n2->n3->n4",
		"account-B": "n1->n2->n5",
		"account-C": "n1->n6->n7",
	}
	for _, path := range paths {
		got := simPathHashes(path)
		if got != want[string(path.Key)] {
			t.Fatalf("path %s = %s, want %s", path.Key, got, want[string(path.Key)])
		}
		if string(path.Key) == "account-A" && path.Nodes[0].ChildCount != 2 {
			t.Fatalf("root ChildCount = %d, want 2", path.Nodes[0].ChildCount)
		}
	}
}

func simulationMPT() *MockMPT {
	n4 := mockNode("n4", "account-A")
	n5 := mockNode("n5", "account-B")
	n7 := mockNode("n7", "account-C")
	n3 := mockBranch("n3", n4)
	n2 := mockBranch("n2", n3, n5)
	n6 := mockBranch("n6", n7)
	n1 := mockBranch("n1", n2, n6)
	return &MockMPT{Root: n1}
}

func simulationCompressOptions(t *testing.T) mptagg.CompressOptions {
	t.Helper()
	params, err := ipa.NewTestParams(64)
	if err != nil {
		t.Fatalf("NewTestParams: %v", err)
	}
	return mptagg.CompressOptions{Params: params}
}

func simCommitmentString(agg *mptagg.AggregatedNode) string {
	return agg.Commitment.String()
}

func mockBranch(hash string, children ...*MockMPTNode) *MockMPTNode {
	return &MockMPTNode{
		Hash:     []byte(hash),
		Size:     10,
		Children: children,
	}
}

func mockNode(hash string, stateKey string) *MockMPTNode {
	return &MockMPTNode{
		Hash:     []byte(hash),
		StateKey: []byte(stateKey),
		LeafHash: []byte("leaf-" + stateKey),
		Size:     10,
	}
}

func rawCount(view *mptagg.LocalPartitionView, pathKey string) int {
	count := 0
	for _, node := range view.Nodes {
		if node.Kind == mptagg.RawMPTNode && string(node.PathKey) == pathKey {
			count++
		}
	}
	return count
}

func hasRaw(view *mptagg.LocalPartitionView, pathKey string, position int) bool {
	for _, node := range view.Nodes {
		if node.Kind == mptagg.RawMPTNode && string(node.PathKey) == pathKey && node.RawNode.Position == position {
			return true
		}
	}
	return false
}

func hasRawReason(view *mptagg.LocalPartitionView, pathKey string, position int, reason string) bool {
	for _, node := range view.Nodes {
		if node.Kind == mptagg.RawMPTNode && string(node.PathKey) == pathKey && node.RawNode.Position == position && node.RawReason == reason {
			return true
		}
	}
	return false
}

func hasAttachedRaw(agg *mptagg.AggregatedNode, position int) bool {
	for _, node := range agg.AttachedRawNodes {
		if node.Position == position {
			return true
		}
	}
	return false
}

func findStatePath(paths []partitioning.StatePath, key []byte) (partitioning.StatePath, bool) {
	for _, path := range paths {
		if bytes.Equal(path.Key, key) {
			return path, true
		}
	}
	return partitioning.StatePath{}, false
}

func simPathHashes(path partitioning.StatePath) string {
	return simNodeHashes(path.Nodes)
}

func simShortPathHashes(path partitioning.StatePath) string {
	return simShortNodeHashes(path.Nodes)
}

func simNodeHashes(nodes []partitioning.PathNode) string {
	out := ""
	for i := range nodes {
		if i > 0 {
			out += "->"
		}
		out += simBytesDisplay(nodes[i].Hash)
	}
	return out
}

func simShortNodeHashes(nodes []partitioning.PathNode) string {
	out := ""
	for i := range nodes {
		if i > 0 {
			out += "->"
		}
		out += simShortHashDisplay(nodes[i].Hash)
	}
	return out
}

func simShortHashDisplay(hash []byte) string {
	if len(hash) == 0 {
		return ""
	}
	if len(hash) <= 3 {
		return simBytesDisplay(hash)
	}
	return "0x" + hex.EncodeToString(hash[:3]) + "..."
}

func simBytesDisplay(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return "0x" + hex.EncodeToString(b)
		}
	}
	return string(b)
}

func simOptimizationConfig() partitioning.OptimizationConfig {
	return partitioning.OptimizationConfig{
		Beta:               1.2,
		HighRiskMultiplier: 1.1,
		MaxSuperNodeLength: 3,
	}
}

func simStatePath(key string, hashes ...string) partitioning.StatePath {
	childCounts := make([]int, len(hashes))
	for i := range hashes {
		childCounts[i] = 1
		if i == len(hashes)-1 {
			childCounts[i] = 0
		}
	}
	return simStatePathWithChildCounts(key, hashes, childCounts)
}

func simStatePathWithChildCounts(key string, hashes []string, childCounts []int) partitioning.StatePath {
	if len(hashes) != len(childCounts) {
		panic("hashes and childCounts length mismatch")
	}
	nodes := make([]partitioning.PathNode, len(hashes))
	nodeHashes := make([][]byte, len(hashes))
	pathNibbles := make([]byte, len(hashes))
	totalSize := 0
	for i := range hashes {
		parent := i - 1
		if i == 0 {
			parent = -1
		}
		nodes[i] = partitioning.PathNode{
			Hash:           []byte(hashes[i]),
			PathKey:        []byte(key),
			Position:       i,
			ParentPosition: parent,
			ChildCount:     childCounts[i],
			Size:           10 + i,
		}
		nodeHashes[i] = []byte(hashes[i])
		pathNibbles[i] = byte(i % 16)
		totalSize += nodes[i].Size
	}
	return partitioning.StatePath{
		Key:         []byte(key),
		PathNibbles: pathNibbles,
		NodeHashes:  nodeHashes,
		Nodes:       nodes,
		LeafHash:    []byte("leaf-" + key),
		Size:        totalSize,
	}
}

func partitionHasPath(partition partitioning.Partition, key string) bool {
	for i := range partition.Paths {
		if string(partition.Paths[i].Key) == key {
			return true
		}
	}
	return false
}

func simPartitionsSummary(partitions []partitioning.Partition) string {
	out := ""
	for i := range partitions {
		if i > 0 {
			out += "|"
		}
		out += fmt.Sprintf("partition-%d:", partitions[i].ID)
		for j := range partitions[i].Paths {
			if j > 0 {
				out += ","
			}
			out += simBytesDisplay(partitions[i].Paths[j].Key)
		}
	}
	return out
}

func simVersionSummary(versions []MPTVersion) string {
	out := ""
	for i := range versions {
		if i > 0 {
			out += "|"
		}
		out += versions[i].RootID
		out += ":"
		out += simPathsSummary(versions[i].Paths)
	}
	return out
}

func simKeysSummary(keys [][]byte) string {
	out := ""
	for i := range keys {
		if i > 0 {
			out += ","
		}
		out += simBytesDisplay(keys[i])
	}
	return out
}

func simRawAvailableHashes(raw map[string]bool) string {
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := ""
	for i, key := range keys {
		if i > 0 {
			out += ","
		}
		out += simShortHashKeyDisplay(key)
	}
	return out
}

func simShortHashKeyDisplay(key string) string {
	const hashPrefix = "hash:"
	if strings.HasPrefix(key, hashPrefix) {
		hash := strings.TrimPrefix(key, hashPrefix)
		if len(hash) > 6 {
			return hashPrefix + "0x" + hash[:6] + "..."
		}
		return hashPrefix + "0x" + hash
	}
	if len(key) > 6 {
		return "0x" + key[:6] + "..."
	}
	return key
}

func simPlanSummary(plans []partitioning.SuperNodeReallocationPlan) string {
	out := ""
	for i := range plans {
		if i > 0 {
			out += "|"
		}
		out += fmt.Sprintf("path=%s nodes=%s preferred=%d assigned=%d reason=%s",
			simBytesDisplay(plans[i].SuperNode.PathKey),
			simNodeHashes(plans[i].SuperNode.Nodes),
			plans[i].PreferredPartition,
			plans[i].AssignedPartition,
			plans[i].Reason)
	}
	return out
}

func simLocalViewSummary(view *mptagg.LocalPartitionView) string {
	out := fmt.Sprintf("partition=%d rawPaths=%s preservedLatest=%s rawSuperNodes=%d",
		view.PartitionID, simKeysSummary(view.RawPathKeys), simKeysSummary(view.PreservedLatestPathKeys), len(view.RawSuperNodes))
	for _, node := range view.Nodes {
		switch node.Kind {
		case mptagg.RawMPTNode:
			out += fmt.Sprintf("|raw:%s:%d:%s", simBytesDisplay(node.PathKey), node.RawNode.Position, simBytesDisplay(node.RawNode.Hash))
		case mptagg.AggregatedCommitmentNode:
			agg := node.Aggregated
			out += fmt.Sprintf("|agg:%s:%s:%s:%s",
				agg.ID,
				simBytesDisplay(agg.PathKey),
				simBytesHashes(agg.OriginalNodeHashes),
				simCommitmentString(agg))
		}
	}
	for _, edge := range view.Edges {
		out += fmt.Sprintf("|edge:%s:%s:%s:%s", simBytesDisplay(edge.PathKey), edge.FromID, edge.ToID, edge.Kind)
	}
	return out
}

func logSimForestState(t *testing.T, label string, state *MPTForestState) {
	t.Helper()
	t.Logf("")
	t.Logf("========== %s ==========", label)
	t.Logf("%s versions=%s latestHeight=%d", label, simVersionSummary(state.Versions), state.LatestHeight)
	t.Logf("%s primary partition summary=%s", label, simPartitionsSummary(state.Partitions))
	t.Logf("%s stored SuperNode candidates=%d stored plans=%d", label, len(state.SuperNodes), len(state.Plans))
	t.Logf("%s SuperNode plans are partitioning outputs; incremental simulation preserves old plans and only appends plans for the newly inserted latest MPT", label)
	t.Logf("%s mptagg produced local views; simulation did not split, compress, or commit paths", label)
	for i, sn := range state.SuperNodes {
		t.Logf("%s SuperNode[%d] path=%s nodes=%s parentPartition=%d", label, i, sn.PathKey, simNodeHashes(sn.Nodes), sn.ParentPartition)
	}
	for i, plan := range state.Plans {
		t.Logf("%s plan[%d] path=%s nodes=%s assigned=%d reason=%s", label, i, plan.SuperNode.PathKey, simNodeHashes(plan.SuperNode.Nodes), plan.AssignedPartition, plan.Reason)
	}

	for _, part := range state.Partitions {
		t.Logf("")
		t.Logf("----- %s partition-%d -----", label, part.ID)
		t.Logf("primary StatePaths owned by partition-%d:", part.ID)
		if len(part.Paths) == 0 {
			t.Logf("  none")
		}
		for _, path := range part.Paths {
			t.Logf("  path=%s nodes=%s", path.Key, simPathHashes(path))
		}

		t.Logf("secondary SuperNodes assigned to partition-%d:", part.ID)
		assigned := 0
		for _, plan := range state.Plans {
			if plan.AssignedPartition != part.ID {
				continue
			}
			assigned++
			t.Logf("  path=%s nodes=%s preferred=%d reason=%s",
				plan.SuperNode.PathKey, simNodeHashes(plan.SuperNode.Nodes), plan.PreferredPartition, plan.Reason)
		}
		if assigned == 0 {
			t.Logf("  none")
		}

		view := state.LocalViews[part.ID]
		if view == nil {
			t.Logf("%s local view partition-%d missing", label, part.ID)
			continue
		}
		t.Logf("local compressed MPT view for partition-%d, built by mptagg:", part.ID)
		t.Logf("  summary rawPrimaryPaths=%d preservedLatest=%s rawSuperNodes=%d aggregatedNodes=%d",
			len(view.RawPathKeys), simKeysSummary(view.PreservedLatestPathKeys), len(view.RawSuperNodes), len(view.AggregatedNodes))
		t.Logf("  rawAvailableHashes=%s", simRawAvailableHashes(view.RawAvailableHashes))
		for _, candidate := range view.CandidateAggregatedNodes {
			t.Logf("  candidate agg id=%s path=%s nibbles=%x hashes=%s",
				candidate.ID, candidate.PathKey, candidate.Nibbles,
				simBytesHashes(candidate.OriginalNodeHashes))
		}
		for _, event := range view.MergeEvents {
			t.Logf("  merge agg key=%s from=%s into=%s nextCount=%d", event.Key, event.FromID, event.IntoID, event.NextCount)
		}
		for _, node := range view.Nodes {
			switch node.Kind {
			case mptagg.RawMPTNode:
				t.Logf("  raw path=%s pos=%d hash=%s reason=%s", node.PathKey, node.RawNode.Position, node.RawNode.Hash, node.RawReason)
			case mptagg.AggregatedCommitmentNode:
				agg := node.Aggregated
				t.Logf("  aggregated id=%s viewPath=%s sourcePath=%s hashes=%s commitment=%s",
					agg.ID, node.PathKey, agg.PathKey, simBytesHashes(agg.OriginalNodeHashes), simCommitmentString(agg))
			}
		}
		for _, edge := range view.Edges {
			t.Logf("  edge path=%s from=%s to=%s kind=%s", edge.PathKey, edge.FromID, edge.ToID, edge.Kind)
		}
	}
}

func logSimForestSummary(t *testing.T, label string, state *MPTForestState) {
	t.Helper()
	t.Logf("")
	t.Logf("========== %s ==========", label)
	t.Logf("%s versions=%s latestHeight=%d", label, simVersionSummary(state.Versions), state.LatestHeight)
	t.Logf("%s first partition result:", label)
	for _, part := range state.Partitions {
		t.Logf("  partition-%d primary StatePaths:", part.ID)
		if len(part.Paths) == 0 {
			t.Logf("    none")
			continue
		}
		for _, path := range part.Paths {
			t.Logf("    %s key=%s hashes=%s", mpttest.LabelForPath(path), simBytesDisplay(path.Key), simShortPathHashes(path))
		}
	}
	t.Logf("%s second partition result / SuperNode reallocation:", label)
	if len(state.Plans) == 0 {
		t.Logf("  none")
	}
	for i, plan := range state.Plans {
		t.Logf("  plan[%d] path=%s positions=[%d,%d] hashes=%s preferred=%d assigned=%d reason=%s",
			i,
			simBytesDisplay(plan.SuperNode.PathKey),
			plan.SuperNode.StartPosition,
			plan.SuperNode.EndPosition,
			simShortNodeHashes(plan.SuperNode.Nodes),
			plan.PreferredPartition,
			plan.AssignedPartition,
			plan.Reason)
	}
}

func logSimFinalLocalCompressedTrees(t *testing.T, state *MPTForestState) {
	t.Helper()
	t.Logf("")
	t.Logf("===== FINAL LOCAL COMPRESSED MPT VIEWS =====")
	var latestReachable map[string]bool
	if len(state.Versions) > 0 {
		latestReachable = BuildLatestReachableSet(state.Versions[len(state.Versions)-1])
	}
	for _, part := range state.Partitions {
		t.Logf("")
		t.Logf("----- partition-%d -----", part.ID)
		view := state.LocalViews[part.ID]
		if view == nil {
			t.Logf("<missing local view>")
			continue
		}
		t.Logf("partition-%d final local compressed MPT:\n%s", part.ID, mptagg.FormatLocalPartitionViewTree(view))
		for _, node := range view.Nodes {
			if node.Kind == mptagg.RawMPTNode && node.RawReason == "preserve-latest" {
				hashKey := hex.EncodeToString(node.RawNode.Hash)
				t.Logf("partition-%d preserve-latest raw hash=%s path=%s pos=%d latestHashMatch=%t",
					part.ID, hashKey, simBytesDisplay(node.PathKey), node.RawNode.Position, latestReachable[hashKey])
			}
		}
	}
}

func recordRootHash(t *testing.T, rootsSeen map[string]bool, label string, rootHash string) {
	t.Helper()
	if rootHash == "" {
		t.Fatalf("%s has empty root hash", label)
	}
	if rootsSeen[rootHash] {
		t.Fatalf("%s root hash was already recorded: %s", label, rootHash)
	}
	rootsSeen[rootHash] = true
	t.Logf("recorded %s root hash: %s", label, rootHash)
}

func logRootAwarePartitionAndAgg(t *testing.T, label string, root *mpttest.TestMPT, result *partitioning.MPTPartitionResult, owned []*mptagg.OwnedRawNodeSet, opts mptagg.CompressOptions) {
	t.Helper()
	t.Logf("")
	t.Logf("========== root-aware manager %s ==========", label)
	t.Logf("%s root=%s previous=%s latestPaths=%d previousPaths=%d", label, result.RootHash, result.PreviousRootHash, len(result.LatestPaths), len(result.PreviousPaths))
	t.Logf("%s root trie structure:\n%s", label, mpttest.FormatTrie(root))
	rootDFSPaths, err := root.ExtractStatePaths()
	if err != nil {
		t.Fatalf("root DFS ExtractStatePaths %s: %v", label, err)
	}
	if len(rootDFSPaths) != len(result.LatestPaths) {
		t.Fatalf("%s root DFS paths = %d, partitioning latest paths = %d", label, len(rootDFSPaths), len(result.LatestPaths))
	}
	t.Logf("%s extracted StatePaths:", label)
	for _, path := range rootDFSPaths {
		t.Logf("  %s key=0x%x nibbles=%s hashes=%s", mpttest.LabelForPath(path), path.Key, mpttest.FormatNibbles(path.PathNibbles), simShortPathHashes(path))
	}
	t.Logf("%s ChangedStatePaths:", label)
	logRootAwareDiffPaths(t, result.Diff.Changed, result.PreviousPaths, "changed")
	t.Logf("%s UnchangedStatePaths:", label)
	logRootAwareDiffPaths(t, result.Diff.Unchanged, result.PreviousPaths, "unchanged")
	t.Logf("%s RemovedStatePaths:", label)
	logRootAwareDiffPaths(t, result.Diff.Removed, result.LatestPaths, "removed")
	t.Logf("%s node hash diff: added=%d reused=%d removed=%d",
		label, len(result.Diff.AddedNodeHashes), len(result.Diff.ReusedNodeHashes), len(result.Diff.RemovedNodeHashes))
	t.Logf("%s primary partitions:", label)
	for _, part := range result.Partitions {
		t.Logf("  partition-%d range=[%d,%d) paths=%d startKey=%s endKey=%s", part.ID, part.StartIndex, part.EndIndex, len(part.Paths), simBytesDisplay(part.StartKey), simBytesDisplay(part.EndKey))
		for _, path := range part.Paths {
			t.Logf("    %s key=0x%x hashes=%s", mpttest.LabelForPath(path), path.Key, simShortPathHashes(path))
		}
	}
	t.Logf("%s SuperNode plans:", label)
	if len(result.Plans) == 0 {
		t.Logf("  none")
	}
	for i, plan := range result.Plans {
		t.Logf("  plan[%d] path=%s positions=[%d,%d] hashes=%s preferred=%d assigned=%d reason=%s",
			i,
			simBytesDisplay(plan.SuperNode.PathKey),
			plan.SuperNode.StartPosition,
			plan.SuperNode.EndPosition,
			simShortNodeHashes(plan.SuperNode.Nodes),
			plan.PreferredPartition,
			plan.AssignedPartition,
			plan.Reason)
	}
	t.Logf("%s RawNodeSets:", label)
	for i, rawSet := range result.RawNodeSets {
		t.Logf("  partition-%d rawNodeHashes=%d %s", i, len(rawSet), rootAwareRawHashSummary(rawSet))
	}
	t.Logf("%s local mptagg views from cumulative local ownership + root DFS:", label)
	for _, part := range result.Partitions {
		view, err := mptagg.BuildLocalPartitionViewFromRootRawNodeSet(root, part.Paths, result.RawNodeSets[part.ID], owned[part.ID], part.ID, opts)
		if err != nil {
			t.Fatalf("BuildLocalPartitionViewFromRootRawNodeSet partition-%d: %v", part.ID, err)
		}
		t.Logf("----- %s partition-%d -----\n%s", label, part.ID, shortenRootAwareCommitments(mptagg.FormatLocalPartitionViewTree(view)))
	}
}

func rootAwareOwnedHandles(partitionCount int) []*mptagg.OwnedRawNodeSet {
	out := make([]*mptagg.OwnedRawNodeSet, partitionCount)
	for i := range out {
		out[i] = mptagg.NewOwnedRawNodeSet()
	}
	return out
}

func rootAwarePathLabels(paths []partitioning.StatePath) string {
	if len(paths) == 0 {
		return "none"
	}
	labels := make([]string, 0, len(paths))
	for _, path := range paths {
		labels = append(labels, mpttest.LabelForPath(path))
	}
	sort.Strings(labels)
	return strings.Join(labels, ",")
}

func logRootAwareDiffPaths(t *testing.T, paths []partitioning.StatePath, compare []partitioning.StatePath, kind string) {
	t.Helper()
	if len(paths) == 0 {
		t.Logf("  none")
		return
	}
	compareByKey := make(map[string]partitioning.StatePath, len(compare))
	for _, path := range compare {
		compareByKey[string(path.Key)] = path
	}
	for _, path := range paths {
		reason := kind
		other, ok := compareByKey[string(path.Key)]
		switch {
		case kind == "changed" && !ok:
			reason = "new-key"
		case kind == "changed" && ok:
			reason = "leaf-changed"
		case kind == "unchanged":
			reason = "leaf-reused"
		case kind == "removed":
			reason = "removed-key"
		}
		if ok && kind == "changed" {
			t.Logf("  %s reason=%s oldLeaf=%s newLeaf=%s", mpttest.LabelForPath(path), reason, rootAwareLeafShort(other), rootAwareLeafShort(path))
			continue
		}
		t.Logf("  %s reason=%s leaf=%s", mpttest.LabelForPath(path), reason, rootAwareLeafShort(path))
	}
}

func rootAwareLeafShort(path partitioning.StatePath) string {
	if len(path.LeafHash) > 0 {
		return shortenRootAwareHash("0x" + hex.EncodeToString(path.LeafHash))
	}
	if len(path.Nodes) > 0 && len(path.Nodes[len(path.Nodes)-1].Hash) > 0 {
		return shortenRootAwareHash("0x" + hex.EncodeToString(path.Nodes[len(path.Nodes)-1].Hash))
	}
	return "<missing>"
}

func rootAwareRawHashSummary(rawSet map[string]bool) string {
	if len(rawSet) == 0 {
		return "[]"
	}
	hashes := make([]string, 0, len(rawSet))
	for hash, keepRaw := range rawSet {
		if keepRaw {
			hashes = append(hashes, shortenRootAwareHash(hash))
		}
	}
	sort.Strings(hashes)
	if len(hashes) > 8 {
		hashes = append(hashes[:8], fmt.Sprintf("...(+%d)", len(hashes)-8))
	}
	return "[" + strings.Join(hashes, ", ") + "]"
}

func shortenRootAwareHash(value string) string {
	if strings.HasPrefix(value, "0x") && len(value) > 7 {
		return value[:7] + "..."
	}
	if len(value) > 5 {
		return value[:5] + "..."
	}
	return value
}

func shortenRootAwareCommitments(text string) string {
	const marker = "commit=("
	var out strings.Builder
	for {
		idx := strings.Index(text, marker)
		if idx < 0 {
			out.WriteString(text)
			return out.String()
		}
		out.WriteString(text[:idx])
		text = text[idx+len(marker):]
		end := strings.IndexByte(text, ')')
		if end < 0 {
			out.WriteString(marker)
			out.WriteString(text)
			return out.String()
		}
		out.WriteString("commit=(")
		out.WriteString(shortenRootAwareCommitmentValue(text[:end]))
		out.WriteByte(')')
		text = text[end+1:]
	}
}

func shortenRootAwareCommitmentValue(value string) string {
	parts := strings.Split(value, ",")
	for i := range parts {
		parts[i] = shortenRootAwareDecimal(strings.TrimSpace(parts[i]))
	}
	return strings.Join(parts, ",")
}

func shortenRootAwareDecimal(value string) string {
	if len(value) <= 5 {
		return value
	}
	return value[:5] + "..."
}

func logSimLatestReachable(t *testing.T, latest MPTVersion) {
	t.Helper()
	reachable := BuildLatestReachableSet(latest)
	t.Logf("latest root=%s height=%d R_latest size=%d", latest.RootID, latest.Height, len(reachable))
	keys := make([]string, 0, len(reachable))
	for hash := range reachable {
		keys = append(keys, hash)
	}
	sort.Strings(keys)
	t.Logf("R_latest hashes:")
	for _, hash := range keys {
		t.Logf("  %s", hash)
	}
	for _, path := range latest.Paths {
		for _, node := range path.Nodes {
			t.Logf("  latest reachable path=%s pos=%d hash=%s", simBytesDisplay(path.Key), node.Position, simBytesDisplay(node.Hash))
		}
	}
}

func simPathsSummary(paths []partitioning.StatePath) string {
	out := ""
	for i := range paths {
		if i > 0 {
			out += ","
		}
		out += simBytesDisplay(paths[i].Key)
		out += "="
		out += simPathHashes(paths[i])
	}
	return out
}

func findSimAggregated(view *mptagg.LocalPartitionView, pathKey string) *mptagg.AggregatedNode {
	for _, node := range view.AggregatedNodes {
		if string(node.PathKey) == pathKey {
			return node
		}
	}
	return nil
}

func aggContainsHash(agg *mptagg.AggregatedNode, hash string) bool {
	for _, got := range agg.OriginalNodeHashes {
		if bytes.Equal(got, []byte(hash)) {
			return true
		}
	}
	return false
}

func simBytesHashes(hashes [][]byte) string {
	out := ""
	for i := range hashes {
		if i > 0 {
			out += "->"
		}
		out += simBytesDisplay(hashes[i])
	}
	return out
}
