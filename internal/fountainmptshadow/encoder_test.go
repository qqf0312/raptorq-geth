package fountainmptshadow

import (
	"bytes"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
)

func TestEncodedPathFoldedFileIPARoundTrip(t *testing.T) {
	nodes := [][]byte{
		{0xf8, 0x01, 0x02},
		bytes.Repeat([]byte{0x23}, 47),
		append([]byte{0xca, 0xfe}, bytes.Repeat([]byte{0x45}, 68)...),
	}
	encoded, err := BuildEncodedPath(
		100,
		common.HexToHash("0x100"),
		[]byte("key-0"),
		nodes,
		[]byte("epoch-10/key-0"),
		4,
	)
	if err != nil {
		t.Fatalf("BuildEncodedPath: %v", err)
	}
	if len(encoded.Matrix) < int(encoded.Layout.SourceCount) || len(encoded.Matrix)&(len(encoded.Matrix)-1) != 0 {
		t.Fatalf("matrix rows %d are not a power-of-two recovery set for %d sources", len(encoded.Matrix), encoded.Layout.SourceCount)
	}
	ok, err := VerifyEncodedPath(encoded)
	if err != nil {
		t.Fatalf("VerifyEncodedPath: %v", err)
	}
	if !ok {
		t.Fatal("folded FileIPA verification failed")
	}
	recovered, err := RecoverPath(encoded)
	if err != nil {
		t.Fatalf("RecoverPath: %v", err)
	}
	if !nodesEqual(recovered, nodes) {
		t.Fatalf("recovered path mismatch")
	}
}

func TestEncodedSingleNodePathStillUsesFoldedRows(t *testing.T) {
	nodes := [][]byte{[]byte("single-leaf-path")}
	encoded, err := BuildEncodedPath(
		1,
		common.HexToHash("0x1"),
		[]byte("only-key"),
		nodes,
		[]byte("single-node-seed"),
		0,
	)
	if err != nil {
		t.Fatalf("BuildEncodedPath: %v", err)
	}
	wantProofRows := nextPowerOfTwo(len(encoded.Matrix) * len(encoded.Blocks[0]))
	if len(encoded.Matrix) != 2 || len(encoded.Proof.Rows) != wantProofRows {
		t.Fatalf("single-node path has %d matrix rows and %d proof rows, want 2 and %d", len(encoded.Matrix), len(encoded.Proof.Rows), wantProofRows)
	}
	ok, err := VerifyEncodedPath(encoded)
	if err != nil {
		t.Fatalf("VerifyEncodedPath: %v", err)
	}
	if !ok {
		t.Fatal("single-node folded FileIPA verification failed")
	}
	recovered, err := RecoverPath(encoded)
	if err != nil {
		t.Fatalf("RecoverPath: %v", err)
	}
	if !nodesEqual(recovered, nodes) {
		t.Fatal("single-node recovered path mismatch")
	}
}

func TestEncodedPathOneRowMode(t *testing.T) {
	nodes := [][]byte{
		bytes.Repeat([]byte{0x11}, 100),
		bytes.Repeat([]byte{0x22}, 300),
		[]byte("leaf"),
	}
	encoded, err := BuildEncodedPath(
		2,
		common.HexToHash("0x2"),
		[]byte("one-row-key"),
		nodes,
		[]byte("one-row-seed"),
		1,
	)
	if err != nil {
		t.Fatalf("BuildEncodedPath: %v", err)
	}
	wantProofRows := nextPowerOfTwo(len(encoded.Blocks[0]))
	if len(encoded.Matrix) != 1 || len(encoded.Proof.Rows) != wantProofRows {
		t.Fatalf("one-row mode has matrix=%d proofRows=%d want=1/%d", len(encoded.Matrix), len(encoded.Proof.Rows), wantProofRows)
	}
	if len(encoded.Proof.FoldProof.Challenges) != 3 {
		t.Fatalf("one vector row should fold eight chunk statements in 3 layers, got %d", len(encoded.Proof.FoldProof.Challenges))
	}
	ok, err := VerifyEncodedPath(encoded)
	if err != nil || !ok {
		t.Fatalf("VerifyEncodedPath: ok=%v err=%v", ok, err)
	}
	if _, err := RecoverPath(encoded); err == nil {
		t.Fatal("one row unexpectedly recovered a multi-source path")
	}
}

func TestHistoricalLeafSelectorRoundTrip(t *testing.T) {
	nodes := [][]byte{
		bytes.Repeat([]byte{0x11}, 20),
		bytes.Repeat([]byte{0x22}, 38),
		append([]byte("leaf-value:"), bytes.Repeat([]byte{0x33}, 70)...),
	}
	leaf, err := BuildHistoricalLeaf(
		91,
		99,
		common.HexToHash("0x99"),
		[]byte("key-0"),
		nodes,
	)
	if err != nil {
		t.Fatalf("BuildHistoricalLeaf: %v", err)
	}
	ok, err := VerifyHistoricalLeaf(leaf)
	if err != nil {
		t.Fatalf("VerifyHistoricalLeaf: %v", err)
	}
	if !ok {
		t.Fatal("historical leaf proof verification failed")
	}
	recovered, err := DecodeHistoricalLeaf(leaf)
	if err != nil {
		t.Fatalf("DecodeHistoricalLeaf: %v", err)
	}
	if !bytes.Equal(recovered, nodes[len(nodes)-1]) {
		t.Fatalf("recovered leaf mismatch")
	}
	if leaf.Proof == nil {
		t.Fatal("historical leaf must use one folded chunk proof")
	}
	wantOnes := len(leaf.Block)
	gotOnes := 0
	for i := range leaf.Selector {
		if !leaf.Selector[i].IsZero() {
			gotOnes++
		}
	}
	if gotOnes != wantOnes {
		t.Fatalf("selector has %d selected chunks, want %d", gotOnes, wantOnes)
	}
}

func TestCompactFramingDoesNotPadEveryNodeToMaximum(t *testing.T) {
	nodes := [][]byte{
		[]byte("short"),
		bytes.Repeat([]byte{0x42}, 400),
		[]byte("leaf"),
	}
	files, layout, err := FramePathNodes(nodes)
	if err != nil {
		t.Fatal(err)
	}
	oldFileSize := roundUp(nodeLengthPrefixSize+len(nodes[1]), SourceChunkSize)
	oldBytes := len(nodes) * oldFileSize
	if got := int(layout.SourceCount * layout.FileSize); got >= oldBytes {
		t.Fatalf("compact source bytes=%d, want less than old per-node padding=%d", got, oldBytes)
	}
	recovered, err := UnframePathNodes(files, layout)
	if err != nil {
		t.Fatal(err)
	}
	if !nodesEqual(recovered, nodes) {
		t.Fatal("compact framing round trip mismatch")
	}
}

func TestEncodedPathRejectsTamperedBlock(t *testing.T) {
	encoded, err := BuildEncodedPath(
		7,
		common.HexToHash("0x7"),
		[]byte("key"),
		[][]byte{[]byte("branch"), []byte("leaf")},
		[]byte("tamper-seed"),
		2,
	)
	if err != nil {
		t.Fatalf("BuildEncodedPath: %v", err)
	}
	var one fr.Element
	one.SetOne()
	encoded.Blocks[0][0].Add(&encoded.Blocks[0][0], &one)
	if ok, err := VerifyEncodedPath(encoded); err == nil || ok {
		t.Fatalf("tampered block accepted: ok=%v err=%v", ok, err)
	}
}

func TestEncodedPathRejectsChunkCancellationTamper(t *testing.T) {
	encoded, err := BuildEncodedPath(
		8,
		common.HexToHash("0x8"),
		[]byte("key"),
		[][]byte{[]byte("branch"), []byte("leaf")},
		[]byte("cancellation-seed"),
		2,
	)
	if err != nil {
		t.Fatalf("BuildEncodedPath: %v", err)
	}
	if len(encoded.Blocks[0]) < 2 {
		t.Fatal("encoded block needs at least two chunks")
	}
	var one fr.Element
	one.SetOne()
	encoded.Blocks[0][0].Add(&encoded.Blocks[0][0], &one)
	encoded.Blocks[0][1].Sub(&encoded.Blocks[0][1], &one)
	if ok, err := VerifyEncodedPath(encoded); err == nil || ok {
		t.Fatalf("sum-preserving chunk tamper accepted: ok=%v err=%v", ok, err)
	}
}

func nodesEqual(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}
