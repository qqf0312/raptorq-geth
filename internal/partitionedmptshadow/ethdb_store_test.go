package partitionedmptshadow

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/internal/ipa"
	"github.com/ethereum/go-ethereum/internal/mptagg"
	"github.com/ethereum/go-ethereum/internal/mpttest"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

func TestEthDBStorePersistsShadowOutputs(t *testing.T) {
	store := NewEthDBStore(rawdb.NewMemoryDatabase())
	manager := newShadowTestManager(4)
	params, err := ipa.NewTestParams(64)
	if err != nil {
		t.Fatalf("NewTestParams: %v", err)
	}
	processor, err := NewProcessor(manager, store, mptagg.CompressOptions{Params: params})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	adapter := &TrieDatabaseAdapter{Processor: processor}

	root, err := mpttest.BuildOrUpdateTestMPT(nil, shadowInitialMPTEntries())
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT: %v", err)
	}
	result, err := adapter.OnDatabaseUpdate(root.DB, root.Root, types.EmptyRootHash, 1, trienode.NewWithNodeSet(root.LastNodeSet), nil)
	if err != nil {
		t.Fatalf("OnDatabaseUpdate: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}

	storedResult, ok, err := store.StoredResult(root.Root)
	if err != nil {
		t.Fatalf("StoredResult: %v", err)
	}
	if !ok {
		t.Fatal("stored result missing")
	}
	if storedResult.Root != root.Root {
		t.Fatalf("stored root = %s, want %s", storedResult.Root, root.Root)
	}
	if storedResult.PartitionCount != 4 {
		t.Fatalf("partition count = %d, want 4", storedResult.PartitionCount)
	}
	if len(storedResult.ChangedPathKeys) != len(result.PartitionResult.Diff.Changed) {
		t.Fatalf("changed keys = %d, want %d", len(storedResult.ChangedPathKeys), len(result.PartitionResult.Diff.Changed))
	}

	for partitionID := range result.PartitionResult.RawNodeSets {
		storedRawSet, ok, err := store.StoredRawSet(root.Root, partitionID)
		if err != nil {
			t.Fatalf("StoredRawSet partition-%d: %v", partitionID, err)
		}
		if !ok {
			t.Fatalf("stored raw set partition-%d missing", partitionID)
		}
		if len(storedRawSet.Hashes) != len(result.PartitionResult.RawNodeSets[partitionID]) {
			t.Fatalf("stored raw set partition-%d hashes = %d, want %d", partitionID, len(storedRawSet.Hashes), len(result.PartitionResult.RawNodeSets[partitionID]))
		}
		if len(storedRawSet.Nodes) == 0 {
			t.Fatalf("stored raw set partition-%d node metadata missing", partitionID)
		}
		for _, node := range storedRawSet.Nodes {
			if len(node.Hash) != 32 {
				t.Fatalf("stored raw set partition-%d node hash len = %d, want 32", partitionID, len(node.Hash))
			}
			if node.BlobSize == 0 {
				t.Fatalf("stored raw set partition-%d node blob size is zero", partitionID)
			}
		}
		storedView, ok, err := store.StoredLocalView(root.Root, partitionID)
		if err != nil {
			t.Fatalf("StoredLocalView partition-%d: %v", partitionID, err)
		}
		if !ok {
			t.Fatalf("stored local view partition-%d missing", partitionID)
		}
		if storedView.PartitionID != uint64(partitionID) {
			t.Fatalf("stored local view partition = %d, want %d", storedView.PartitionID, partitionID)
		}
	}
}

func TestEthDBStorePersistsOnlyNodePartitionShadowOutputs(t *testing.T) {
	const nodePartition = 1

	store := NewEthDBStore(rawdb.NewMemoryDatabase())
	manager := newShadowTestManager(4)
	params, err := ipa.NewTestParams(64)
	if err != nil {
		t.Fatalf("NewTestParams: %v", err)
	}
	processor, err := NewProcessor(manager, store, mptagg.CompressOptions{Params: params})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	processor.SetNodePartition(nodePartition)
	adapter := &TrieDatabaseAdapter{Processor: processor}

	root, err := mpttest.BuildOrUpdateTestMPT(nil, shadowInitialMPTEntries())
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT: %v", err)
	}
	result, err := adapter.OnDatabaseUpdate(root.DB, root.Root, types.EmptyRootHash, 1, trienode.NewWithNodeSet(root.LastNodeSet), nil)
	if err != nil {
		t.Fatalf("OnDatabaseUpdate: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}
	if len(result.PartitionResult.Partitions) != 4 {
		t.Fatalf("partition result partitions = %d, want 4", len(result.PartitionResult.Partitions))
	}
	if len(result.LocalViews) != 1 || result.LocalViews[nodePartition] == nil {
		t.Fatalf("local views = %v, want only partition-%d", result.LocalViews, nodePartition)
	}

	storedResult, ok, err := store.StoredResult(root.Root)
	if err != nil {
		t.Fatalf("StoredResult: %v", err)
	}
	if !ok {
		t.Fatal("stored result missing")
	}
	if storedResult.PartitionCount != 4 {
		t.Fatalf("partition count = %d, want 4", storedResult.PartitionCount)
	}
	if len(storedResult.PartitionIDs) != 1 || storedResult.PartitionIDs[0] != uint64(nodePartition) {
		t.Fatalf("stored partition ids = %v, want [%d]", storedResult.PartitionIDs, nodePartition)
	}

	for partitionID := range result.PartitionResult.RawNodeSets {
		_, rawOK, err := store.StoredRawSet(root.Root, partitionID)
		if err != nil {
			t.Fatalf("StoredRawSet partition-%d: %v", partitionID, err)
		}
		_, viewOK, err := store.StoredLocalView(root.Root, partitionID)
		if err != nil {
			t.Fatalf("StoredLocalView partition-%d: %v", partitionID, err)
		}
		wantStored := partitionID == nodePartition
		if rawOK != wantStored {
			t.Fatalf("partition-%d raw stored = %v, want %v", partitionID, rawOK, wantStored)
		}
		if viewOK != wantStored {
			t.Fatalf("partition-%d local view stored = %v, want %v", partitionID, viewOK, wantStored)
		}
	}
}

func TestPartitionNodeDBStorePersistsRawAndCompressedNodes(t *testing.T) {
	store := NewEthDBStore(rawdb.NewMemoryDatabase())
	partitionDB := rawdb.NewMemoryDatabase()
	manager := newShadowTestManager(4)
	params, err := ipa.NewTestParams(64)
	if err != nil {
		t.Fatalf("NewTestParams: %v", err)
	}
	processor, err := NewProcessor(manager, store, mptagg.CompressOptions{Params: params})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	adapter := &TrieDatabaseAdapter{
		Processor:      processor,
		PartitionNodes: NewPartitionNodeDBStore(partitionDB),
	}

	root, err := mpttest.BuildOrUpdateTestMPT(nil, shadowInitialMPTEntries())
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT: %v", err)
	}
	result, err := adapter.OnDatabaseUpdate(root.DB, root.Root, types.EmptyRootHash, 1, trienode.NewWithNodeSet(root.LastNodeSet), nil)
	if err != nil {
		t.Fatalf("OnDatabaseUpdate: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}

	var checkedRaw bool
	for _, view := range result.LocalViews {
		root.LastNodeSet.ForEachWithOrder(func(_ string, node *trienode.Node) {
			if checkedRaw || node == nil || node.IsDeleted() || !ownedRawHash(view.RawAvailableHashes, node.Hash) {
				return
			}
			got := rawdb.ReadLegacyTrieNode(partitionDB, node.Hash)
			if string(got) != string(node.Blob) {
				t.Fatalf("partition raw node mismatch for %s", node.Hash)
			}
			checkedRaw = true
		})
	}
	if !checkedRaw {
		t.Fatal("no owned dirty raw node checked")
	}

	var checkedCompressed bool
	for _, view := range result.LocalViews {
		for _, agg := range view.AggregatedNodes {
			if len(agg.OriginalNodeHashes) == 0 {
				continue
			}
			boundary := common.BytesToHash(agg.OriginalNodeHashes[0])
			stored, ok, err := ReadPartitionCompressedNode(partitionDB, boundary)
			if err != nil {
				t.Fatalf("ReadPartitionCompressedNode: %v", err)
			}
			if !ok {
				t.Fatalf("compressed node for boundary %s missing", boundary)
			}
			if len(stored.Commitment) == 0 {
				t.Fatal("compressed node commitment missing")
			}
			if len(agg.OriginalNodeHashes) > 1 && len(stored.CoveredHashes) == 0 {
				t.Fatal("compressed node covered hashes missing")
			}
			checkedCompressed = true
			break
		}
		if checkedCompressed {
			break
		}
	}
	if !checkedCompressed {
		t.Fatal("no compressed node checked")
	}
}

func TestPartitionNodeDBStoreCanReadRawPathLeafValue(t *testing.T) {
	store := NewEthDBStore(rawdb.NewMemoryDatabase())
	partitionDB := rawdb.NewMemoryDatabase()
	manager := newShadowTestManager(1)
	params, err := ipa.NewTestParams(64)
	if err != nil {
		t.Fatalf("NewTestParams: %v", err)
	}
	processor, err := NewProcessor(manager, store, mptagg.CompressOptions{Params: params})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	adapter := &TrieDatabaseAdapter{
		Processor:      processor,
		PartitionNodes: NewPartitionNodeDBStore(partitionDB),
	}

	root, err := mpttest.BuildOrUpdateTestMPT(nil, shadowInitialMPTEntries())
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT: %v", err)
	}
	result, err := adapter.OnDatabaseUpdate(root.DB, root.Root, types.EmptyRootHash, 1, trienode.NewWithNodeSet(root.LastNodeSet), nil)
	if err != nil {
		t.Fatalf("OnDatabaseUpdate: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}

	wantValues := make(map[string][]byte, len(root.Entries))
	for _, entry := range root.Entries {
		wantValues[string(entry.Key)] = append([]byte(nil), entry.Value...)
	}
	partitionTrieDB := trie.NewDatabase(partitionDB, nil)
	partitionTrie, err := trie.New(trie.TrieID(root.Root), partitionTrieDB)
	if err != nil {
		t.Fatalf("open partition trie: %v", err)
	}
	for _, entry := range root.Entries {
		want, ok := wantValues[string(entry.Key)]
		if !ok {
			continue
		}
		got, err := partitionTrie.Get(entry.Key)
		if err != nil {
			t.Fatalf("partition trie get key %x: %v", entry.Key, err)
		}
		if string(got) != string(want) {
			t.Fatalf("partition trie value mismatch for key %x: got %x want %x", entry.Key, got, want)
		}
		return
	}
	t.Fatal("no leaf key checked")
}
