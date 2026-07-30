package fountainmptshadow

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
)

// TestFullEpochFlow is an executable description of the complete storage
// phase: collect roots and key versions, finalize the latest path with folded
// matrix rows, persist historical leaf selector proofs, then load and verify
// every artifact without the original source paths.
func TestFullEpochFlow(t *testing.T) {
	const (
		epochStart = uint64(5)
		epochEnd   = uint64(10)
	)
	key := []byte("account-key-0")
	db := rawdb.NewMemoryDatabase()
	store := NewEthDBStore(db)
	processor, err := NewProcessor(ProcessorConfig{
		EpochStart:  epochStart,
		Seed:        []byte("full-flow-epoch-seed"),
		MinimumRows: 4,
	}, store)
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}

	version0Path := [][]byte{
		[]byte("root-5/branch"),
		[]byte("root-5/leaf:value-v0"),
	}
	version1Path := [][]byte{
		[]byte("root-8/branch"),
		[]byte("root-8/extension"),
		[]byte("root-8/leaf:value-v1"),
	}
	finalPath := [][]byte{
		[]byte("root-10/branch"),
		[]byte("root-10/extension"),
		[]byte("root-10/leaf:value-v2"),
	}
	paths := mapPathResolver{
		pathMapKey(rootForProcessorHeight(5), key):  version0Path,
		pathMapKey(rootForProcessorHeight(8), key):  version1Path,
		pathMapKey(rootForProcessorHeight(10), key): finalPath,
	}

	t.Logf("epoch=[%d,%d] key=%q", epochStart, epochEnd, key)
	for height := epochStart; height <= epochEnd; height++ {
		var updates []KeyUpdate
		switch height {
		case 6, 9:
			updates = []KeyUpdate{{
				Key:            key,
				PreviousExists: true,
				NewExists:      true,
			}}
		}
		root := rootForProcessorHeight(height)
		if err := processor.RecordHeight(height, root, updates); err != nil {
			t.Fatalf("RecordHeight(%d): %v", height, err)
		}
		t.Logf("height=%d root=%s keyUpdated=%t", height, root.Hex(), len(updates) != 0)
	}

	result, err := processor.Finalize(epochEnd, [][]byte{key}, paths)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	t.Logf("finalize roots=%d versions=%d latestPaths=%d historicalLeaves=%d",
		result.RootCount,
		result.VersionCount,
		result.EncodedPathCount,
		result.HistoricalLeafCount,
	)
	pathBlob, err := db.Get(pathKey(rootForProcessorHeight(epochEnd), key))
	if err != nil {
		t.Fatal(err)
	}
	storedPath, err := decodeCompactPath(pathBlob)
	if err != nil {
		t.Fatal(err)
	}
	aggregateBlob, err := db.Get(aggregateKey(storedPath.AggregateHeight, storedPath.AggregateID))
	if err != nil {
		t.Fatal(err)
	}
	compactAggregate, err := decodeCompactAggregate(aggregateBlob)
	if err != nil {
		t.Fatal(err)
	}
	if compactAggregate.PathRoot != rootForProcessorHeight(epochEnd) ||
		!bytes.Equal(compactAggregate.PathSeedBase, []byte("full-flow-epoch-seed")) {
		t.Fatalf("aggregate did not persist shared path derivation metadata")
	}
	for _, relation := range compactAggregate.Relations {
		if relation.Kind == AggregateRelationPath &&
			(relation.Root != (common.Hash{}) || len(relation.Seed) != 0 ||
				relation.Offset != 0 || relation.SourceCount != 0 ||
				relation.Row != 0 || relation.RowCount != 0) {
			t.Fatalf("path relation retained derivable metadata: %+v", relation)
		}
	}
	for _, segment := range compactAggregate.Segments {
		if segment.Offset != 0 {
			t.Fatalf("segment retained derivable offset %d", segment.Offset)
		}
	}
	paths = nil
	store = NewEthDBStore(db)
	t.Log("sourcePathResolver=discarded")

	wantRoots := map[uint64]struct{}{}
	for height := epochStart; height <= epochEnd; height++ {
		root, ok, err := store.Root(height)
		if err != nil || !ok {
			t.Fatalf("load root %d: ok=%v err=%v", height, ok, err)
		}
		wantRoots[height] = struct{}{}
		t.Logf("storedRoot height=%d root=%s", height, root.Hex())
	}
	if len(wantRoots) != int(epochEnd-epochStart+1) {
		t.Fatalf("stored root count %d", len(wantRoots))
	}

	finalRoot := rootForProcessorHeight(epochEnd)
	encoded, ok, err := store.EncodedPath(finalRoot, key)
	if err != nil || !ok {
		t.Fatalf("load latest encoded path: ok=%v err=%v", ok, err)
	}
	verified, err := VerifyEncodedPath(encoded)
	if err != nil || !verified {
		t.Fatalf("verify latest encoded path: ok=%v err=%v", verified, err)
	}
	recovered, err := RecoverPath(encoded)
	if err != nil {
		t.Fatalf("recover latest path: %v", err)
	}
	if !nodesEqual(recovered, finalPath) {
		t.Fatal("latest recovered path mismatch")
	}
	t.Logf("latest root=%s Q=%s", encoded.Root.Hex(), encoded.Q.String())
	t.Logf("latest layout nodes=%d sources=%d compactBytes=%d symbolBytes=%d matrix=%dx%d encodedBlocks=%dx%d",
		encoded.Layout.NodeCount,
		encoded.Layout.SourceCount,
		encoded.Layout.CompactBytes,
		encoded.Layout.FileSize,
		len(encoded.Matrix),
		len(encoded.Matrix[0]),
		len(encoded.Blocks),
		len(encoded.Blocks[0]),
	)
	for row := range encoded.Matrix {
		relationStart := int(encoded.AggregateStart) + row*len(encoded.Blocks[row])
		chunkCs := make([]fr.Element, len(encoded.Blocks[row]))
		for chunk := range chunkCs {
			chunkCs[chunk] = encoded.Aggregate.Relations[relationStart+chunk].C
		}
		t.Logf("latest row=%d A=%s block=%s chunkCs=%s",
			row,
			formatFlowScalars(encoded.Matrix[row]),
			formatFlowScalars(encoded.Blocks[row]),
			formatFlowScalars(chunkCs),
		)
	}
	t.Logf("epoch aggregate relations=%d foldedRows=%d foldLayers=%d ipaRounds=%d verified=%t recoveredNodes=%d",
		len(encoded.Aggregate.Relations),
		len(encoded.Aggregate.Proof.Rows),
		len(encoded.Aggregate.Proof.FoldProof.Challenges),
		len(encoded.Aggregate.Proof.IPAProof.L),
		verified,
		len(recovered),
	)

	historicalCases := []struct {
		start    uint64
		terminal uint64
		leaf     []byte
	}{
		{start: 5, terminal: 5, leaf: version0Path[len(version0Path)-1]},
		{start: 6, terminal: 8, leaf: version1Path[len(version1Path)-1]},
	}
	for _, history := range historicalCases {
		root := rootForProcessorHeight(history.terminal)
		leaf, ok, err := store.HistoricalLeaf(root, key)
		if err != nil || !ok {
			t.Fatalf("load historical leaf at %d: ok=%v err=%v", history.terminal, ok, err)
		}
		verified, err := VerifyHistoricalLeaf(leaf)
		if err != nil || !verified {
			t.Fatalf("verify historical leaf at %d: ok=%v err=%v", history.terminal, verified, err)
		}
		recoveredLeaf, err := DecodeHistoricalLeaf(leaf)
		if err != nil {
			t.Fatalf("decode historical leaf at %d: %v", history.terminal, err)
		}
		if !bytes.Equal(recoveredLeaf, history.leaf) {
			t.Fatalf("historical leaf at %d mismatch", history.terminal)
		}
		t.Logf("history version=[%d,%d] terminalRoot=%s selector=%s",
			history.start,
			history.terminal,
			root.Hex(),
			formatFlowScalars(leaf.Selector),
		)
		relation := leaf.Aggregate.Relations[leaf.AggregateIndex]
		t.Logf("history aggregateIndex=%d segmentQ=%s block=%s C=%s aggregateRounds=%d verified=%t leaf=%q",
			leaf.AggregateIndex,
			leaf.Q.String(),
			formatFlowScalars(leaf.Block),
			relation.C.String(),
			len(leaf.Aggregate.Proof.IPAProof.L),
			verified,
			recoveredLeaf,
		)
	}
	audit, err := store.Audit()
	if err != nil {
		t.Fatalf("audit aggregate storage: %v", err)
	}
	if audit.AggregateProofs != 1 || len(audit.Paths) != 1 || len(audit.HistoricalLeaves) != 2 {
		t.Fatalf("unexpected aggregate audit summary: %+v", audit)
	}
	for _, path := range audit.Paths {
		if !path.Verified {
			t.Fatal("aggregate path audit failed")
		}
	}
	for _, history := range audit.HistoricalLeaves {
		if !history.Verified {
			t.Fatal("aggregate historical audit failed")
		}
	}
	t.Log("source paths can now be discarded by the simulation; verification used only persisted matrix/blocks/Q/proofs")
}

func formatFlowScalars(values []fr.Element) string {
	parts := make([]string, len(values))
	for i := range values {
		parts[i] = fmt.Sprintf("%s", values[i].String())
	}
	return "[" + strings.Join(parts, ",") + "]"
}
