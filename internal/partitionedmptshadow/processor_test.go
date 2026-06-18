package partitionedmptshadow

import (
	"bytes"
	"fmt"
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/internal/ipa"
	"github.com/ethereum/go-ethereum/internal/mptagg"
	"github.com/ethereum/go-ethereum/internal/mpttest"
	"github.com/ethereum/go-ethereum/internal/partitioning"
)

func TestProcessorStoresIncrementalPartitionsAndLocalViews(t *testing.T) {
	manager := &partitioning.MPTPartitionManager{
		PartitionCount: 2,
		SortBy:         partitioning.SortByKey,
		Config: partitioning.OptimizationConfig{
			Beta:               1.2,
			HighRiskMultiplier: 1.1,
			MaxSuperNodeLength: 3,
		},
		PreserveLatest:    false,
		PreserveLatestSet: true,
	}
	params, err := ipa.NewTestParams(64)
	if err != nil {
		t.Fatalf("NewTestParams: %v", err)
	}
	store := NewMemoryStore()
	processor, err := NewProcessor(manager, store, mptagg.CompressOptions{Params: params})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}

	root1, err := mpttest.BuildOrUpdateTestMPT(nil, []mpttest.TestTrieEntry{
		{Name: "A", Key: []byte{0x12, 0x34}, Value: []byte("value-A")},
		{Name: "B", Key: []byte{0x12, 0x56}, Value: []byte("value-B")},
	})
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT root1: %v", err)
	}
	changed1, err := root1.ExtractStatePaths()
	if err != nil {
		t.Fatalf("ExtractStatePaths root1: %v", err)
	}
	result1, err := processor.OnRootUpdate(Input{Root: root1.Root, View: root1, ChangedPaths: changed1})
	if err != nil {
		t.Fatalf("OnRootUpdate root1: %v", err)
	}
	if result1 == nil || len(result1.LocalViews) != 2 {
		t.Fatalf("root1 local views = %d, want 2", len(result1.LocalViews))
	}

	root2, err := mpttest.BuildOrUpdateTestMPT(root1, []mpttest.TestTrieEntry{
		{Name: "C", Key: []byte{0x16, 0xab}, Value: []byte("value-C")},
	})
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT root2: %v", err)
	}
	latest2, err := root2.ExtractStatePaths()
	if err != nil {
		t.Fatalf("ExtractStatePaths root2: %v", err)
	}
	diff2 := partitioning.DiffStatePaths(changed1, latest2)
	result2, err := processor.OnRootUpdate(Input{Root: root2.Root, Parent: root1.Root, View: root2, ChangedPaths: diff2.Changed})
	if err != nil {
		t.Fatalf("OnRootUpdate root2: %v", err)
	}
	if result2 == nil {
		t.Fatalf("nil root2 result")
	}
	if len(result2.PartitionResult.Partitions) == 0 {
		t.Fatalf("root2 partitions empty")
	}
	if len(result2.LocalViews) != len(result2.PartitionResult.Partitions) {
		t.Fatalf("root2 local views = %d, partitions = %d", len(result2.LocalViews), len(result2.PartitionResult.Partitions))
	}
	if stored, ok := store.Result(root2.Root); !ok || stored != result2 {
		t.Fatalf("root2 result not stored")
	}
}

func TestProcessorMatchesManualFourPartitionPartitioningAndCompression(t *testing.T) {
	const partitionCount = 4
	processorManager := newShadowTestManager(partitionCount)
	manualManager := newShadowTestManager(partitionCount)
	params, err := ipa.NewTestParams(64)
	if err != nil {
		t.Fatalf("NewTestParams: %v", err)
	}
	opts := mptagg.CompressOptions{Params: params}
	processorStore := NewMemoryStore()
	processor, err := NewProcessor(processorManager, processorStore, opts)
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	manualStore := NewMemoryStore()

	root1, err := mpttest.BuildOrUpdateTestMPT(nil, shadowInitialMPTEntries())
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT root1: %v", err)
	}
	result1 := assertShadowRoundMatchesManual(t, "root1", processor, manualManager, manualStore, root1, nil, partitionCount, opts)

	root2, err := mpttest.BuildOrUpdateTestMPT(root1, shadowUpdateMPTEntries())
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT root2: %v", err)
	}
	result2 := assertShadowRoundMatchesManual(t, "root2", processor, manualManager, manualStore, root2, result1.PartitionResult.LatestPaths, partitionCount, opts)

	root3, err := mpttest.BuildOrUpdateTestMPT(root2, shadowSecondUpdateMPTEntries())
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT root3: %v", err)
	}
	result3 := assertShadowRoundMatchesManual(t, "root3", processor, manualManager, manualStore, root3, result2.PartitionResult.LatestPaths, partitionCount, opts)
	if len(result3.PartitionResult.Partitions) != partitionCount {
		t.Fatalf("root3 partitions = %d, want %d", len(result3.PartitionResult.Partitions), partitionCount)
	}
}

func assertShadowRoundMatchesManual(t *testing.T, label string, processor *Processor, manualManager *partitioning.MPTPartitionManager, manualStore *MemoryStore, root *mpttest.TestMPT, previousPaths []partitioning.StatePath, partitionCount int, opts mptagg.CompressOptions) *Result {
	t.Helper()
	latestPaths, err := root.ExtractStatePaths()
	if err != nil {
		t.Fatalf("%s ExtractStatePaths: %v", label, err)
	}
	diff := partitioning.DiffStatePaths(previousPaths, latestPaths)
	got, err := processor.OnRootUpdate(Input{Root: root.Root, View: root, ChangedPaths: diff.Changed})
	if err != nil {
		t.Fatalf("%s OnRootUpdate: %v", label, err)
	}
	if got == nil {
		t.Fatalf("%s nil shadow result", label)
	}
	wantPartitionResult, err := manualManager.AddLatestRoot(root)
	if err != nil {
		t.Fatalf("%s manual AddLatestRoot: %v", label, err)
	}
	if got.PartitionResult.RootHash != wantPartitionResult.RootHash {
		t.Fatalf("%s root hash = %s, want %s", label, got.PartitionResult.RootHash, wantPartitionResult.RootHash)
	}
	if gotPartitions := shadowPartitionSummary(got.PartitionResult.Partitions); gotPartitions != shadowPartitionSummary(wantPartitionResult.Partitions) {
		t.Fatalf("%s partitions = %s, want %s", label, gotPartitions, shadowPartitionSummary(wantPartitionResult.Partitions))
	}
	if len(got.PartitionResult.RawNodeSets) != len(wantPartitionResult.RawNodeSets) {
		t.Fatalf("%s raw set count = %d, want %d", label, len(got.PartitionResult.RawNodeSets), len(wantPartitionResult.RawNodeSets))
	}
	for partitionID := 0; partitionID < partitionCount; partitionID++ {
		if !stringBoolMapsEqual(got.PartitionResult.RawNodeSets[partitionID], wantPartitionResult.RawNodeSets[partitionID]) {
			t.Fatalf("%s partition-%d rawSet = %s, want %s",
				label,
				partitionID,
				shadowRawSetSummary(got.PartitionResult.RawNodeSets[partitionID]),
				shadowRawSetSummary(wantPartitionResult.RawNodeSets[partitionID]))
		}
		gotStoredRawSet, ok := processor.Store().RawNodeSet(root.Root, partitionID)
		if !ok {
			t.Fatalf("%s partition-%d rawSet not stored", label, partitionID)
		}
		if !stringBoolMapsEqual(gotStoredRawSet, wantPartitionResult.RawNodeSets[partitionID]) {
			t.Fatalf("%s partition-%d stored rawSet mismatch", label, partitionID)
		}
		manualOwned := mergeRawNodeSets(manualStore.HistoricalRawNodeSet(partitionID), wantPartitionResult.RawNodeSets[partitionID])
		wantView, err := mptagg.BuildLocalPartitionViewFromRawNodeSet(wantPartitionResult.LatestPaths, manualOwned, partitionID, opts)
		if err != nil {
			t.Fatalf("%s manual BuildLocalPartitionViewFromRawNodeSet partition-%d: %v", label, partitionID, err)
		}
		if err := manualStore.SaveRawNodeSet(root.Root, partitionID, wantPartitionResult.RawNodeSets[partitionID]); err != nil {
			t.Fatalf("%s manual SaveRawNodeSet partition-%d: %v", label, partitionID, err)
		}
		gotView := got.LocalViews[partitionID]
		if gotView == nil {
			t.Fatalf("%s partition-%d missing local view", label, partitionID)
		}
		gotViewSummary := shadowLocalViewSummary(gotView)
		wantViewSummary := shadowLocalViewSummary(wantView)
		if gotViewSummary != wantViewSummary {
			t.Fatalf("%s partition-%d local view mismatch\ngot:  %s\nwant: %s", label, partitionID, gotViewSummary, wantViewSummary)
		}
	}
	return got
}

func newShadowTestManager(partitionCount int) *partitioning.MPTPartitionManager {
	return &partitioning.MPTPartitionManager{
		PartitionCount: partitionCount,
		SortBy:         partitioning.SortByKey,
		Config: partitioning.OptimizationConfig{
			Beta:               1.2,
			HighRiskMultiplier: 1.1,
			MaxSuperNodeLength: 3,
		},
		PreserveLatest:    false,
		PreserveLatestSet: true,
	}
}

func shadowInitialMPTEntries() []mpttest.TestTrieEntry {
	return []mpttest.TestTrieEntry{
		{Name: "A", Key: []byte{0x12, 0x34}, Value: shadowLongValue("A-v1")},
		{Name: "B", Key: []byte{0x12, 0x56}, Value: shadowLongValue("B-v1")},
		{Name: "C", Key: []byte{0x16, 0xab}, Value: shadowLongValue("C-v1")},
	}
}

func shadowUpdateMPTEntries() []mpttest.TestTrieEntry {
	return []mpttest.TestTrieEntry{
		{Name: "Z", Key: []byte{0x12, 0x88}, Value: shadowLongValue("Z-v2")},
		{Name: "Y", Key: []byte{0x18, 0xff}, Value: shadowLongValue("Y-v2")},
	}
}

func shadowSecondUpdateMPTEntries() []mpttest.TestTrieEntry {
	return []mpttest.TestTrieEntry{
		{Name: "A", Key: []byte{0x12, 0x34}, Value: shadowLongValue("A-v3")},
		{Name: "W", Key: []byte{0x11, 0xee}, Value: shadowLongValue("W-v3")},
	}
}

func shadowLongValue(label string) []byte {
	return []byte("value-" + label + "-kept-long-enough-to-make-leaf-nodes-standalone")
}

func shadowPartitionSummary(partitions []partitioning.Partition) string {
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
			out += fmt.Sprintf("0x%x", partitions[i].Paths[j].Key)
		}
	}
	return out
}

func shadowLocalViewSummary(view *mptagg.LocalPartitionView) string {
	out := fmt.Sprintf("partition=%d", view.PartitionID)
	for _, node := range view.Nodes {
		switch node.Kind {
		case mptagg.RawMPTNode:
			out += fmt.Sprintf("|raw:%x:%d:%x:%s", node.PathKey, node.RawNode.Position, node.RawNode.Hash, node.RawReason)
		case mptagg.AggregatedCommitmentNode:
			agg := node.Aggregated
			out += fmt.Sprintf("|agg:%s:%x:%x:%s:%d", agg.ID, agg.PathKey, agg.Nibbles, shadowBytesHashes(agg.OriginalNodeHashes), len(agg.AttachedRawNodes))
			for _, attached := range agg.AttachedRawNodes {
				out += fmt.Sprintf(":attached:%d:%x:%x", attached.Position, attached.PathKey, attached.Hash)
			}
		}
	}
	for _, edge := range view.Edges {
		out += fmt.Sprintf("|edge:%x:%s:%s:%s", edge.PathKey, edge.FromID, edge.ToID, edge.Kind)
	}
	return out
}

func shadowBytesHashes(values [][]byte) string {
	out := ""
	for i := range values {
		if i > 0 {
			out += "->"
		}
		out += fmt.Sprintf("%x", values[i])
	}
	return out
}

func shadowRawSetSummary(rawSet map[string]bool) string {
	keys := make([]string, 0, len(rawSet))
	for key, keep := range rawSet {
		if keep {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return fmt.Sprintf("%v", keys)
}

func stringBoolMapsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func statePathsEqual(a, b []partitioning.StatePath) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i].Key, b[i].Key) {
			return false
		}
	}
	return true
}
