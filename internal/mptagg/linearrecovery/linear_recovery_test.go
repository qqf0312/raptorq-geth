package linearrecovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/internal/mptagg"
)

func TestSingleNodeRecoveryAllTargets(t *testing.T) {
	nodeCount := 3
	b := []fr.Element{testFE(10), testFE(20), testFE(30)}
	rows := makeLinearRows(t, [][]fr.Element{
		{testFE(1), testFE(1), testFE(0)},
		{testFE(0), testFE(1), testFE(1)},
		{testFE(1), testFE(0), testFE(1)},
	}, b)

	for targetIndex := 0; targetIndex < nodeCount; targetIndex++ {
		canRecover, err := CanRecoverSingleNode(rows, nodeCount, targetIndex)
		if err != nil {
			t.Fatalf("CanRecoverSingleNode target %d: %v", targetIndex, err)
		}
		if !canRecover {
			t.Fatalf("target %d should be recoverable", targetIndex)
		}

		lambda, err := SolveSingleNodeRecoveryWeights(rows, nodeCount, targetIndex)
		if err != nil {
			t.Fatalf("SolveSingleNodeRecoveryWeights target %d: %v", targetIndex, err)
		}
		if !checkRecoveryWeights(rows, nodeCount, targetIndex, lambda) {
			t.Fatalf("lambda does not reconstruct e_%d: lambda=%s", targetIndex, formatFrVector(lambda))
		}

		recovered, recoveredLambda, err := RecoverSingleNode(rows, nodeCount, targetIndex)
		if err != nil {
			t.Fatalf("RecoverSingleNode target %d: %v", targetIndex, err)
		}
		if !frSlicesEqual(recoveredLambda, lambda) {
			t.Fatalf("RecoverSingleNode lambda mismatch: got %s want %s", formatFrVector(recoveredLambda), formatFrVector(lambda))
		}
		if !recovered.Equal(&b[targetIndex]) {
			t.Fatalf("recovered target %d mismatch: got %s want %s", targetIndex, formatFr(recovered), formatFr(b[targetIndex]))
		}
	}
}

func TestSingleNodeRecoveryUnrecoverable(t *testing.T) {
	rows := []LinearEncodedRow{
		{A: []fr.Element{testFE(1), testFE(1), testFE(0)}, C: testFE(30)},
	}

	canRecover, err := CanRecoverSingleNode(rows, 3, 2)
	if err != nil {
		t.Fatalf("CanRecoverSingleNode: %v", err)
	}
	if canRecover {
		t.Fatalf("target 2 should not be recoverable")
	}

	if _, err := SolveSingleNodeRecoveryWeights(rows, 3, 2); !errors.Is(err, ErrSingleNodeNotRecoverable) {
		t.Fatalf("SolveSingleNodeRecoveryWeights error = %v, want ErrSingleNodeNotRecoverable", err)
	}
	if _, _, err := RecoverSingleNode(rows, 3, 2); !errors.Is(err, ErrSingleNodeNotRecoverable) {
		t.Fatalf("RecoverSingleNode error = %v, want ErrSingleNodeNotRecoverable", err)
	}
}

func TestSingleNodeRecoveryDoesNotRequireFullRank(t *testing.T) {
	rows := []LinearEncodedRow{
		{A: []fr.Element{testFE(1), testFE(0), testFE(0)}, C: testFE(10)},
	}

	canRecover, err := CanRecoverSingleNode(rows, 3, 0)
	if err != nil {
		t.Fatalf("CanRecoverSingleNode target 0: %v", err)
	}
	if !canRecover {
		t.Fatalf("target 0 should be recoverable")
	}
	recovered, lambda, err := RecoverSingleNode(rows, 3, 0)
	if err != nil {
		t.Fatalf("RecoverSingleNode target 0: %v", err)
	}
	if !checkRecoveryWeights(rows, 3, 0, lambda) {
		t.Fatalf("lambda does not reconstruct e_0: lambda=%s", formatFrVector(lambda))
	}
	if !recovered.Equal(&rows[0].C) {
		t.Fatalf("recovered target 0 mismatch: got %s want %s", formatFr(recovered), formatFr(rows[0].C))
	}

	canRecover, err = CanRecoverSingleNode(rows, 3, 1)
	if err != nil {
		t.Fatalf("CanRecoverSingleNode target 1: %v", err)
	}
	if canRecover {
		t.Fatalf("target 1 should not be recoverable")
	}
}

func TestSingleNodeRecoveryIgnoresPaddingDimensions(t *testing.T) {
	rows := []LinearEncodedRow{
		{A: []fr.Element{testFE(1), testFE(0), testFE(0), testFE(0), testFE(0), testFE(0), testFE(0), testFE(0)}, C: testFE(10)},
	}

	canRecover, err := CanRecoverSingleNode(rows, 3, 0)
	if err != nil {
		t.Fatalf("CanRecoverSingleNode: %v", err)
	}
	if !canRecover {
		t.Fatalf("target 0 should be recoverable with padded A")
	}
	recovered, lambda, err := RecoverSingleNode(rows, 3, 0)
	if err != nil {
		t.Fatalf("RecoverSingleNode: %v", err)
	}
	if !checkRecoveryWeights(rows, 3, 0, lambda) {
		t.Fatalf("lambda does not reconstruct e_0: lambda=%s", formatFrVector(lambda))
	}
	if !recovered.Equal(&rows[0].C) {
		t.Fatalf("recovered mismatch: got %s want %s", formatFr(recovered), formatFr(rows[0].C))
	}
}

func TestSingleNodeRecoveryInvalidInputs(t *testing.T) {
	validRows := []LinearEncodedRow{
		{A: []fr.Element{testFE(1), testFE(0), testFE(0)}, C: fr.Element{}},
	}
	tests := []struct {
		name        string
		rows        []LinearEncodedRow
		nodeCount   int
		targetIndex int
	}{
		{name: "empty rows", rows: nil, nodeCount: 3, targetIndex: 0},
		{name: "non-positive nodeCount", rows: validRows, nodeCount: 0, targetIndex: 0},
		{name: "negative targetIndex", rows: validRows, nodeCount: 3, targetIndex: -1},
		{name: "targetIndex out of range", rows: validRows, nodeCount: 3, targetIndex: 3},
		{name: "short row", rows: []LinearEncodedRow{{A: []fr.Element{testFE(1), testFE(0)}, C: fr.Element{}}}, nodeCount: 3, targetIndex: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := CanRecoverSingleNode(tt.rows, tt.nodeCount, tt.targetIndex); err == nil {
				t.Fatalf("CanRecoverSingleNode should reject invalid input")
			}
			if _, err := SolveSingleNodeRecoveryWeights(tt.rows, tt.nodeCount, tt.targetIndex); err == nil {
				t.Fatalf("SolveSingleNodeRecoveryWeights should reject invalid input")
			}
			if _, _, err := RecoverSingleNode(tt.rows, tt.nodeCount, tt.targetIndex); err == nil {
				t.Fatalf("RecoverSingleNode should reject invalid input")
			}
		})
	}

	recovered, lambda, err := RecoverSingleNode(validRows, 3, 0)
	if err != nil {
		t.Fatalf("RecoverSingleNode should handle zero C: %v", err)
	}
	if !recovered.IsZero() {
		t.Fatalf("zero C should recover zero, got %s", formatFr(recovered))
	}
	if !checkRecoveryWeights(validRows, 3, 0, lambda) {
		t.Fatalf("lambda does not reconstruct e_0: lambda=%s", formatFrVector(lambda))
	}
}

func TestSingleNodeRecoveryVerbose(t *testing.T) {
	nodeCount := 3
	targetIndex := 0
	b := []fr.Element{testFE(10), testFE(20), testFE(30)}
	rows := makeLinearRows(t, [][]fr.Element{
		{testFE(1), testFE(1), testFE(0)},
		{testFE(0), testFE(1), testFE(1)},
		{testFE(1), testFE(0), testFE(1)},
	}, b)

	t.Logf("nodeCount=%d targetIndex=%d", nodeCount, targetIndex)
	for i := range rows {
		t.Logf("row[%d] A=%s C=%s", i, formatFrVector(rows[i].A), formatFr(rows[i].C))
	}

	matrix := transposeRows(rows, nodeCount)
	rhs := unitVector(nodeCount, targetIndex)
	t.Logf("constructed linear system A^T lambda = e_target")
	for i := range matrix {
		t.Logf("A^T[%d]=%s rhs=%s", i, formatFrVector(matrix[i]), formatFr(rhs[i]))
	}

	lambda, ok, pivots, err := solveLinearSystemFrWithPivots(matrix, rhs)
	if err != nil {
		t.Fatalf("solveLinearSystemFrWithPivots: %v", err)
	}
	if !ok {
		t.Fatalf("target should be recoverable")
	}
	for _, pivot := range pivots {
		t.Logf("pivot row=%d col=%d", pivot.Row, pivot.Col)
	}
	t.Logf("lambda=%s", formatFrVector(lambda))

	check := multiplyLambdaByRows(rows, nodeCount, lambda)
	t.Logf("lambda^T A=%s", formatFrVector(check))
	if !checkRecoveryWeights(rows, nodeCount, targetIndex, lambda) {
		t.Fatalf("lambda does not reconstruct e_%d", targetIndex)
	}

	recovered, recoveredLambda, err := RecoverSingleNode(rows, nodeCount, targetIndex)
	if err != nil {
		t.Fatalf("RecoverSingleNode: %v", err)
	}
	t.Logf("RecoverSingleNode lambda=%s", formatFrVector(recoveredLambda))
	t.Logf("recovered=sum_j lambda_j*C_j=%s", formatFr(recovered))
	t.Logf("expected target node=%s", formatFr(b[targetIndex]))
	if !recovered.Equal(&b[targetIndex]) {
		t.Fatalf("final result mismatch: got %s want %s", formatFr(recovered), formatFr(b[targetIndex]))
	}
	t.Logf("final result=ok")
}

func TestRecoverSingleEncodedMPTNodeBytes(t *testing.T) {
	nodes := []encodedTestNode{
		{path: []byte("aaa"), value: []byte("xxx")},
		{path: []byte("bbb"), value: []byte("yyy")},
		{path: []byte("ccc"), value: []byte("zzz")},
	}
	t.Logf("STEP 1 original nodes")
	mptNodes := make([]mptagg.MPTNodeLike, len(nodes))
	for i := range nodes {
		mptNodes[i] = nodes[i]
		t.Logf("  node[%d].path=%q", i, nodes[i].path)
		t.Logf("  node[%d].value=%q", i, nodes[i].value)
		t.Logf("  node[%d].hash=%x", i, nodes[i].Hash())
	}

	t.Logf("STEP 2 node -> bytes")
	canonicalByNode := make([][]byte, len(nodes))
	for i := range nodes {
		encoded, err := nodes[i].Encode()
		if err != nil {
			t.Fatalf("Encode node %d: %v", i, err)
		}
		canonicalByNode[i] = appendCanonicalSingleNode(nil, encoded)
		if len(canonicalByNode[i]) > 31 {
			t.Fatalf("test node %d should fit in one fr chunk, got %d bytes", i, len(canonicalByNode[i]))
		}
		t.Logf("  node[%d].Encode len=%d hex=%x", i, len(encoded), encoded)
		t.Logf("  canonicalNode[%d]=uint64(len(node[%d].Encode)) || node[%d].Encode", i, i, i)
		t.Logf("  canonicalNode[%d] len=%d hex=%x", i, len(canonicalByNode[i]), canonicalByNode[i])
	}

	canonical, err := mptagg.CanonicalPathBytes(mptNodes)
	if err != nil {
		t.Fatalf("CanonicalPathBytes: %v", err)
	}
	expectedCanonical := bytes.Join(canonicalByNode, nil)
	if !bytes.Equal(canonical, expectedCanonical) {
		t.Fatalf("canonical path bytes mismatch: got %x want %x", canonical, expectedCanonical)
	}
	t.Logf("  full canonical path len=%d hex=%x", len(canonical), canonical)

	chunks, err := mptagg.BytesToFrChunks(canonical)
	if err != nil {
		t.Fatalf("BytesToFrChunks: %v", err)
	}
	if len(chunks) != len(nodes) {
		t.Fatalf("chunk count = %d, want %d", len(chunks), len(nodes))
	}
	t.Logf("STEP 3 bytes -> fr source vector B")
	t.Logf("  B has %d chunk(s)", len(chunks))
	for i := range chunks {
		t.Logf("  B[%d] encodes node[%d] and equals %s", i, i, formatFr(chunks[i]))
	}

	rows := []LinearEncodedRow{
		{A: []fr.Element{testFE(1), testFE(1), testFE(0)}, C: innerProductForTest([]fr.Element{testFE(1), testFE(1), testFE(0)}, chunks)},
		{A: []fr.Element{testFE(0), testFE(1), testFE(1)}, C: innerProductForTest([]fr.Element{testFE(0), testFE(1), testFE(1)}, chunks)},
		{A: []fr.Element{testFE(1), testFE(0), testFE(1)}, C: innerProductForTest([]fr.Element{testFE(1), testFE(0), testFE(1)}, chunks)},
	}
	aMatrix := make([][]fr.Element, len(rows))
	cVector := make([]fr.Element, len(rows))
	for i := range rows {
		aMatrix[i] = append([]fr.Element(nil), rows[i].A...)
		cVector[i] = rows[i].C
	}
	t.Logf("STEP 4 build linear encoded rows C = A * B")
	t.Logf("  A matrix has %d row(s), %d node dimension(s)", len(rows), len(chunks))
	for i := range rows {
		t.Logf("  A[%d]=%s", i, formatFrVector(rows[i].A))
		t.Logf("  C[%d]=<A[%d],B>=%s", i, i, formatFr(rows[i].C))
	}

	t.Logf("STEP 5 solve A^T * lambda = e_target")
	for targetIndex := range nodes {
		t.Logf("  targetIndex=%d e_target=%s", targetIndex, formatFrVector(unitVector(len(chunks), targetIndex)))
		result, err := RecoverSingleDecodedNodeFromMatrix[encodedTestNode](
			aMatrix,
			cVector,
			targetIndex,
			len(canonicalByNode[targetIndex]),
			decodeSingleCanonicalTestNode,
		)
		if err != nil {
			t.Fatalf("RecoverSingleDecodedNodeFromMatrix target %d: %v", targetIndex, err)
		}
		recovered := result.Value
		lambda := result.Lambda
		t.Logf("  lambda=%s", formatFrVector(lambda))
		weightCheck := multiplyLambdaByRows(rows, len(chunks), lambda)
		t.Logf("  lambda^T A=%s", formatFrVector(weightCheck))
		for rowIndex := range rows {
			t.Logf("  lambda[%d]*A[%d]=%s * %s", rowIndex, rowIndex, formatFr(lambda[rowIndex]), formatFrVector(rows[rowIndex].A))
		}
		if !checkRecoveryWeights(rows, len(chunks), targetIndex, lambda) {
			t.Fatalf("lambda does not reconstruct target %d: lambda=%s", targetIndex, formatFrVector(lambda))
		}

		t.Logf("STEP 6 recover target node[%d] fr = sum(lambda[j] * C[j])", targetIndex)
		for rowIndex := range rows {
			var term fr.Element
			term.Mul(&lambda[rowIndex], &rows[rowIndex].C)
			t.Logf("  term[%d]=lambda[%d]*C[%d]=%s", rowIndex, rowIndex, rowIndex, formatFr(term))
		}
		t.Logf("  recovered node[%d] fr=%s", targetIndex, formatFr(recovered))
		t.Logf("  expected node[%d] fr=B[%d]=%s", targetIndex, targetIndex, formatFr(chunks[targetIndex]))
		if !recovered.Equal(&chunks[targetIndex]) {
			t.Fatalf("recovered chunk mismatch for target %d: got %s want %s", targetIndex, formatFr(recovered), formatFr(chunks[targetIndex]))
		}

		t.Logf("STEP 7 recovered node[%d] fr -> bytes -> node", targetIndex)
		recoveredBytes := result.Bytes
		t.Logf("  recovered canonicalNode[%d] bytes len=%d hex=%x", targetIndex, len(recoveredBytes), recoveredBytes)
		bytesMatch := bytes.Equal(recoveredBytes, canonicalByNode[targetIndex])
		t.Logf("  bytes match original canonicalNode[%d]=%t", targetIndex, bytesMatch)
		if !bytesMatch {
			t.Fatalf("recovered canonical node %d bytes mismatch: got %x want %x", targetIndex, recoveredBytes, canonicalByNode[targetIndex])
		}

		recoveredNode := result.Node
		t.Logf("STEP 8 final node[%d] comparison", targetIndex)
		t.Logf("  recovered.path=%q", recoveredNode.path)
		t.Logf("  recovered.value=%q", recoveredNode.value)
		t.Logf("  recovered.hash=%x", recoveredNode.Hash())
		t.Logf("  path matches=%t", bytes.Equal(recoveredNode.path, nodes[targetIndex].path))
		t.Logf("  value matches=%t", bytes.Equal(recoveredNode.value, nodes[targetIndex].value))
		t.Logf("  hash matches=%t", bytes.Equal(recoveredNode.Hash(), nodes[targetIndex].Hash()))
		if !bytes.Equal(recoveredNode.path, nodes[targetIndex].path) || !bytes.Equal(recoveredNode.value, nodes[targetIndex].value) || !bytes.Equal(recoveredNode.Hash(), nodes[targetIndex].Hash()) {
			t.Fatalf("recovered node %d does not match original node", targetIndex)
		}
	}
}

func makeLinearRows(t *testing.T, vectors [][]fr.Element, b []fr.Element) []LinearEncodedRow {
	t.Helper()
	rows := make([]LinearEncodedRow, len(vectors))
	for i := range vectors {
		if len(vectors[i]) != len(b) {
			t.Fatalf("vector length %d does not match B length %d", len(vectors[i]), len(b))
		}
		rows[i] = LinearEncodedRow{
			A: append([]fr.Element(nil), vectors[i]...),
			C: innerProductForTest(vectors[i], b),
		}
	}
	return rows
}

func innerProductForTest(a, b []fr.Element) fr.Element {
	var out fr.Element
	for i := range a {
		var term fr.Element
		term.Mul(&a[i], &b[i])
		out.Add(&out, &term)
	}
	return out
}

func scalarMulForTest(a, b fr.Element) fr.Element {
	var out fr.Element
	out.Mul(&a, &b)
	return out
}

func checkRecoveryWeights(rows []LinearEncodedRow, nodeCount int, targetIndex int, lambda []fr.Element) bool {
	if len(lambda) != len(rows) {
		return false
	}
	check := multiplyLambdaByRows(rows, nodeCount, lambda)
	for i := 0; i < nodeCount; i++ {
		want := fr.Element{}
		if i == targetIndex {
			want.SetOne()
		}
		if !check[i].Equal(&want) {
			return false
		}
	}
	return true
}

func multiplyLambdaByRows(rows []LinearEncodedRow, nodeCount int, lambda []fr.Element) []fr.Element {
	out := make([]fr.Element, nodeCount)
	for j := range rows {
		for i := 0; i < nodeCount; i++ {
			var term fr.Element
			term.Mul(&lambda[j], &rows[j].A[i])
			out[i].Add(&out[i], &term)
		}
	}
	return out
}

func transposeRows(rows []LinearEncodedRow, nodeCount int) [][]fr.Element {
	matrix := make([][]fr.Element, nodeCount)
	for i := 0; i < nodeCount; i++ {
		matrix[i] = make([]fr.Element, len(rows))
		for j := range rows {
			matrix[i][j] = rows[j].A[i]
		}
	}
	return matrix
}

func unitVector(n int, index int) []fr.Element {
	out := make([]fr.Element, n)
	out[index].SetOne()
	return out
}

func frSlicesEqual(a, b []fr.Element) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Equal(&b[i]) {
			return false
		}
	}
	return true
}

func testFE(v uint64) fr.Element {
	var out fr.Element
	out.SetUint64(v)
	return out
}

func formatFr(e fr.Element) string {
	return e.String()
}

func formatFrVector(v []fr.Element) string {
	out := "["
	for i := range v {
		if i > 0 {
			out += ", "
		}
		out += formatFr(v[i])
	}
	return out + "]"
}

type encodedTestNode struct {
	path  []byte
	value []byte
}

func (n encodedTestNode) Encode() ([]byte, error) {
	out := make([]byte, 0, 1+8+len(n.path)+8+len(n.value))
	out = append(out, 0x01)
	out = appendTestLengthPrefixed(out, n.path)
	out = appendTestLengthPrefixed(out, n.value)
	return out, nil
}

func (n encodedTestNode) Hash() []byte {
	encoded, _ := n.Encode()
	sum := sha256.Sum256(encoded)
	return sum[:]
}

func (n encodedTestNode) PathFragment() []byte {
	return append([]byte(nil), n.path...)
}

func appendTestLengthPrefixed(out []byte, data []byte) []byte {
	var lenBuf [8]byte
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(data)))
	out = append(out, lenBuf[:]...)
	out = append(out, data...)
	return out
}

func appendCanonicalSingleNode(out []byte, encoded []byte) []byte {
	var lenBuf [8]byte
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(encoded)))
	out = append(out, lenBuf[:]...)
	out = append(out, encoded...)
	return out
}

func decodeSingleCanonicalTestNode(canonical []byte) (encodedTestNode, error) {
	if len(canonical) < 8 {
		return encodedTestNode{}, fmt.Errorf("canonical bytes too short: %d", len(canonical))
	}
	encodedLen := int(binary.BigEndian.Uint64(canonical[:8]))
	if encodedLen != len(canonical)-8 {
		return encodedTestNode{}, fmt.Errorf("encoded length prefix %d does not match payload length %d", encodedLen, len(canonical)-8)
	}
	return decodeTestNode(canonical[8:])
}

func decodeTestNode(encoded []byte) (encodedTestNode, error) {
	if len(encoded) == 0 {
		return encodedTestNode{}, fmt.Errorf("empty encoded node")
	}
	if encoded[0] != 0x01 {
		return encodedTestNode{}, fmt.Errorf("unexpected node tag 0x%x", encoded[0])
	}
	rest := encoded[1:]
	path, rest, err := readTestLengthPrefixed(rest)
	if err != nil {
		return encodedTestNode{}, fmt.Errorf("decode path: %w", err)
	}
	value, rest, err := readTestLengthPrefixed(rest)
	if err != nil {
		return encodedTestNode{}, fmt.Errorf("decode value: %w", err)
	}
	if len(rest) != 0 {
		return encodedTestNode{}, fmt.Errorf("trailing bytes: %d", len(rest))
	}
	return encodedTestNode{path: path, value: value}, nil
}

func readTestLengthPrefixed(in []byte) ([]byte, []byte, error) {
	if len(in) < 8 {
		return nil, nil, fmt.Errorf("missing length prefix")
	}
	n := int(binary.BigEndian.Uint64(in[:8]))
	if n > len(in)-8 {
		return nil, nil, fmt.Errorf("declared length %d exceeds remaining bytes %d", n, len(in)-8)
	}
	data := append([]byte(nil), in[8:8+n]...)
	return data, in[8+n:], nil
}
