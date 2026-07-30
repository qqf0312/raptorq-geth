package fountainmptshadow

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
)

func TestEthDBStoreRoundTrip(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	store := NewEthDBStore(db)
	root := common.HexToHash("0x1234")
	key := []byte("key-0")
	nodes := [][]byte{
		[]byte("branch"),
		bytes.Repeat([]byte{0x44}, 48),
		[]byte("leaf-value"),
	}
	encoded, err := BuildEncodedPath(10, root, key, nodes, []byte("store-seed"), 4)
	if err != nil {
		t.Fatalf("BuildEncodedPath: %v", err)
	}
	leaf, err := BuildHistoricalLeaf(5, 9, common.HexToHash("0x999"), key, nodes)
	if err != nil {
		t.Fatalf("BuildHistoricalLeaf: %v", err)
	}
	if err := store.SaveRoots([]RootRecord{{Height: 10, Root: root}}); err != nil {
		t.Fatalf("SaveRoots: %v", err)
	}
	if err := store.SaveEncodedPath(encoded); err != nil {
		t.Fatalf("SaveEncodedPath: %v", err)
	}
	if err := store.SaveHistoricalLeaf(leaf); err != nil {
		t.Fatalf("SaveHistoricalLeaf: %v", err)
	}
	baseline := NodeUpdateStats{Height: 10, Nodes: 3, BlobBytes: 100, HashBytes: 96, Deletes: 1}
	if err := store.SaveNodeUpdateStats(baseline); err != nil {
		t.Fatalf("SaveNodeUpdateStats: %v", err)
	}
	storedRoot, ok, err := store.Root(10)
	if err != nil || !ok || storedRoot != root {
		t.Fatalf("Root: root=%s ok=%v err=%v", storedRoot, ok, err)
	}
	storedPath, ok, err := store.EncodedPath(root, key)
	if err != nil || !ok {
		t.Fatalf("EncodedPath: ok=%v err=%v", ok, err)
	}
	verified, err := VerifyEncodedPath(storedPath)
	if err != nil || !verified {
		t.Fatalf("VerifyEncodedPath: ok=%v err=%v", verified, err)
	}
	recovered, err := RecoverPath(storedPath)
	if err != nil {
		t.Fatalf("RecoverPath: %v", err)
	}
	if !nodesEqual(recovered, nodes) {
		t.Fatal("stored encoded path recovery mismatch")
	}
	storedLeaf, ok, err := store.HistoricalLeaf(leaf.TerminalRoot, key)
	if err != nil || !ok {
		t.Fatalf("HistoricalLeaf: ok=%v err=%v", ok, err)
	}
	verified, err = VerifyHistoricalLeaf(storedLeaf)
	if err != nil || !verified {
		t.Fatalf("VerifyHistoricalLeaf: ok=%v err=%v", verified, err)
	}
	recoveredLeaf, err := DecodeHistoricalLeaf(storedLeaf)
	if err != nil {
		t.Fatalf("DecodeHistoricalLeaf: %v", err)
	}
	if !bytes.Equal(recoveredLeaf, nodes[len(nodes)-1]) {
		t.Fatal("stored historical leaf recovery mismatch")
	}
	audit, err := store.Audit()
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if audit.Roots != 1 || len(audit.Paths) != 1 || len(audit.HistoricalLeaves) != 1 {
		t.Fatalf("unexpected audit summary: %+v", audit)
	}
	if !audit.Paths[0].Verified || !audit.HistoricalLeaves[0].Verified {
		t.Fatalf("audit did not verify stored proofs: %+v", audit)
	}
	sizes, err := store.StorageBreakdown()
	if err != nil {
		t.Fatalf("StorageBreakdown: %v", err)
	}
	if sizes.Baseline.Nodes != baseline.Nodes ||
		sizes.Baseline.BlobBytes != baseline.BlobBytes ||
		sizes.Baseline.HashBytes != baseline.HashBytes ||
		sizes.Baseline.Deletes != baseline.Deletes {
		t.Fatalf("baseline=%+v want cumulative values from %+v", sizes.Baseline, baseline)
	}
	if sizes.EncodedBlocksBytes == 0 ||
		sizes.HistoricalLeafBytes == 0 ||
		sizes.CommitmentBytes == 0 ||
		sizes.ProofBytes == 0 ||
		sizes.RootRecordBytes == 0 ||
		sizes.OtherRecordMetadataBytes == 0 ||
		sizes.RecordBytes == 0 {
		t.Fatalf("incomplete storage breakdown: %+v", sizes)
	}
	if sizes.GenerationMatrixBytes != 0 {
		t.Fatalf("generation matrix was persisted: %+v", sizes)
	}
	if sizes.HistoricalLeafBytes != uint64(len(nodes[len(nodes)-1])) {
		t.Fatalf("historical leaf bytes=%d want raw leaf bytes=%d", sizes.HistoricalLeafBytes, len(nodes[len(nodes)-1]))
	}
	wantStatements := uint64(len(encoded.Proof.Rows)+len(leaf.Proof.Rows)) * 32
	if sizes.ProofStatementBytes != wantStatements {
		t.Fatalf("persisted statement bytes=%d want only C scalars=%d", sizes.ProofStatementBytes, wantStatements)
	}
}

func TestEthDBStoreReusesCanonicalRoots(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	root := common.HexToHash("0x1234")
	store := NewEthDBStoreWithRootResolver(db, func(height uint64) (common.Hash, bool) {
		return root, height == 10
	})
	if err := store.SaveRoots([]RootRecord{{Height: 10, Root: root}}); err != nil {
		t.Fatal(err)
	}
	if has, err := db.Has(rootKey(10)); err != nil || has {
		t.Fatalf("sidecar duplicated canonical root: has=%v err=%v", has, err)
	}
	got, ok, err := store.Root(10)
	if err != nil || !ok || got != root {
		t.Fatalf("resolved root=%s ok=%v err=%v", got, ok, err)
	}
}

func TestAggregateSurvivesLaterPathRecordOverwrite(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	store := NewEthDBStore(db)
	root := common.HexToHash("0xabc")
	key := []byte("same-root-key")
	nodes := [][]byte{
		[]byte("branch"),
		bytes.Repeat([]byte{0x55}, 300),
		[]byte("leaf"),
	}
	firstPath, err := BuildEncodedPath(10, root, key, nodes, []byte("first-seed"), 1)
	if err != nil {
		t.Fatal(err)
	}
	firstLeaf, err := BuildHistoricalLeaf(5, 9, common.HexToHash("0x999"), key, nodes)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := FramePathNodes(nodes)
	if err != nil {
		t.Fatal(err)
	}
	firstAggregate, err := buildEpochAggregate(10, []aggregateSegment{
		{path: firstPath, files: files},
		{leaf: firstLeaf, files: files},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveEncodedPath(firstPath); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveHistoricalLeaf(firstLeaf); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveEpochAggregate(firstAggregate); err != nil {
		t.Fatal(err)
	}
	pathBlob, err := db.Get(pathKey(root, key))
	if err != nil {
		t.Fatal(err)
	}
	compactPath, err := decodeCompactPath(pathBlob)
	if err != nil {
		t.Fatal(err)
	}
	if len(compactPath.Blocks) != 0 {
		t.Fatalf("aggregated path duplicated %d encoded rows", len(compactPath.Blocks))
	}
	if compactPath.Root != (common.Hash{}) || compactPath.SourceCount != 0 ||
		len(compactPath.Seed) != 0 || len(compactPath.Key) != 0 {
		t.Fatalf("aggregated path duplicated aggregate metadata: %+v", compactPath)
	}
	leafBlob, err := db.Get(historicalKey(firstLeaf.TerminalRoot, key))
	if err != nil {
		t.Fatal(err)
	}
	compactLeaf, err := decodeCompactHistory(leafBlob)
	if err != nil {
		t.Fatal(err)
	}
	if compactLeaf.TerminalRoot != (common.Hash{}) || compactLeaf.SourceCount != 0 ||
		compactLeaf.AggregateIndex != 0 || len(compactLeaf.Key) != 0 {
		t.Fatalf("historical leaf duplicated aggregate metadata: %+v", compactLeaf)
	}
	aggregateBlob, err := db.Get(aggregateKey(firstAggregate.Height, epochAggregateID(firstAggregate)))
	if err != nil {
		t.Fatal(err)
	}
	compactAggregate, err := decodeCompactAggregate(aggregateBlob)
	if err != nil {
		t.Fatal(err)
	}
	pathRelations := 0
	for _, relation := range compactAggregate.Relations {
		if relation.Kind == AggregateRelationPath {
			pathRelations += SourceSymbolSize / SourceChunkSize
		}
	}
	if len(compactAggregate.RowCs) != pathRelations {
		t.Fatalf("stored C scalars=%d want path relations only=%d", len(compactAggregate.RowCs), pathRelations)
	}
	if len(compactAggregate.Relations) >= len(firstAggregate.Relations) {
		t.Fatalf("relations were not grouped: stored=%d expanded=%d",
			len(compactAggregate.Relations), len(firstAggregate.Relations))
	}

	laterPath, err := BuildEncodedPath(14, root, key, nodes, []byte("later-seed"), 1)
	if err != nil {
		t.Fatal(err)
	}
	laterAggregate, err := buildEpochAggregate(14, []aggregateSegment{{path: laterPath, files: files}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveEncodedPath(laterPath); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveEpochAggregate(laterAggregate); err != nil {
		t.Fatal(err)
	}

	reopened := NewEthDBStore(db)
	historical, ok, err := reopened.HistoricalLeaf(firstLeaf.TerminalRoot, key)
	if err != nil || !ok {
		t.Fatalf("load historical leaf: ok=%v err=%v", ok, err)
	}
	verified, err := VerifyHistoricalLeaf(historical)
	if err != nil || !verified {
		t.Fatalf("old aggregate after overwrite: verified=%v err=%v", verified, err)
	}
}
