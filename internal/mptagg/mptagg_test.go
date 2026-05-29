package mptagg

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/internal/ipa"
)

type mockMPTNode struct {
	path  []byte
	value []byte
}

func newMockMPTNode(path, value string) mockMPTNode {
	return mockMPTNode{
		path:  []byte(path),
		value: []byte(value),
	}
}

func (n mockMPTNode) Encode() ([]byte, error) {
	out := make([]byte, 0, 1+8+len(n.path)+8+len(n.value))
	out = append(out, 0x01)
	out = appendLengthPrefixed(out, n.path)
	out = appendLengthPrefixed(out, n.value)
	return out, nil
}

func (n mockMPTNode) Hash() []byte {
	encoded, _ := n.Encode()
	sum := sha256.Sum256(encoded)
	return sum[:]
}

func (n mockMPTNode) PathFragment() []byte {
	return append([]byte(nil), n.path...)
}

func TestCompressPathToAggregatedNode(t *testing.T) {
	path := mockPath(
		newMockMPTNode("root", "branch"),
		newMockMPTNode("n1", "alpha"),
		newMockMPTNode("n2", "beta"),
		newMockMPTNode("n3", "leaf"),
	)
	opts := testOptionsForPaths(t, path)

	node, err := CompressPathToAggregatedNode(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	if node.NodeCount != len(path) {
		t.Fatalf("NodeCount = %d, want %d", node.NodeCount, len(path))
	}
	if len(node.OriginalNodeHashes) != len(path) {
		t.Fatalf("hash count = %d, want %d", len(node.OriginalNodeHashes), len(path))
	}
	if node.Next != nil || len(node.NextHash) != 0 {
		t.Fatal("unexpected Next for full path compression")
	}
	if node.Commitment.IsInfinity() {
		t.Fatal("commitment is empty")
	}
	ok, err := VerifyAggregatedNode(node, path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected aggregated node to verify")
	}

	tampered := clonePath(path)
	tampered[2] = newMockMPTNode("n2", "tampered")
	ok, err = VerifyAggregatedNode(node, tampered)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("tampered path verified")
	}
}

func TestCompressBranches(t *testing.T) {
	branches := [][]MPTNodeLike{
		mockPath(newMockMPTNode("a1", "one"), newMockMPTNode("a2", "two"), newMockMPTNode("a3", "three")),
		mockPath(newMockMPTNode("b1", "four"), newMockMPTNode("b2", "five")),
		mockPath(newMockMPTNode("c1", "six"), newMockMPTNode("c2", "seven"), newMockMPTNode("c3", "eight"), newMockMPTNode("c4", "nine")),
	}
	opts := testOptionsForPaths(t, branches...)

	nodes, err := CompressBranches(branches, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != len(branches) {
		t.Fatalf("aggregated node count = %d, want %d", len(nodes), len(branches))
	}
	for i := range nodes {
		ok, err := VerifyAggregatedNode(nodes[i], branches[i])
		if err != nil {
			t.Fatalf("verify branch %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("branch %d did not verify", i)
		}
	}
	for i := range nodes {
		for j := i + 1; j < len(nodes); j++ {
			if nodes[i].Commitment.Equal(&nodes[j].Commitment) {
				t.Fatalf("branch commitments %d and %d unexpectedly match", i, j)
			}
		}
	}

	tampered := make([][]MPTNodeLike, len(branches))
	copy(tampered, branches)
	tampered[1] = clonePath(branches[1])
	tampered[1][0] = newMockMPTNode("b1", "changed")
	for i := range nodes {
		ok, err := VerifyAggregatedNode(nodes[i], tampered[i])
		if err != nil {
			t.Fatalf("verify tampered branch %d: %v", i, err)
		}
		if i == 1 && ok {
			t.Fatal("tampered branch verified")
		}
		if i != 1 && !ok {
			t.Fatalf("untampered branch %d failed verification", i)
		}
	}
}

func TestCommitmentStability(t *testing.T) {
	path := mockPath(
		newMockMPTNode("n1", "alpha"),
		newMockMPTNode("n2", "beta"),
		newMockMPTNode("n3", "gamma"),
	)
	changedContent := clonePath(path)
	changedContent[1] = newMockMPTNode("n2", "changed")
	changedOrder := mockPath(
		newMockMPTNode("n2", "beta"),
		newMockMPTNode("n1", "alpha"),
		newMockMPTNode("n3", "gamma"),
	)
	changedCount := mockPath(
		newMockMPTNode("n1", "alpha"),
		newMockMPTNode("n2", "beta"),
	)
	opts := testOptionsForPaths(t, path, changedContent, changedOrder, changedCount)

	first := mustCompress(t, opts, path)
	second := mustCompress(t, opts, path)
	if !first.Commitment.Equal(&second.Commitment) {
		t.Fatal("same path produced different commitments")
	}

	for name, variant := range map[string][]MPTNodeLike{
		"content": changedContent,
		"order":   changedOrder,
		"count":   changedCount,
	} {
		node := mustCompress(t, opts, variant)
		if first.Commitment.Equal(&node.Commitment) {
			t.Fatalf("commitment did not change after %s change", name)
		}
	}
}

func TestStopPolicyNoStopCompressesToLeaf(t *testing.T) {
	path := mockPath(
		newMockMPTNode("n1", "one"),
		newMockMPTNode("n2", "two"),
		newMockMPTNode("leaf", "three"),
	)
	opts := testOptionsForPaths(t, path)

	node, err := CompressPathToAggregatedNode(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	if node.NodeCount != len(path) {
		t.Fatalf("NodeCount = %d, want %d", node.NodeCount, len(path))
	}
	if node.Next != nil || len(node.NextHash) != 0 {
		t.Fatal("expected compression to reach leaf with nil Next")
	}
	ok, err := VerifyAggregatedNode(node, path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected no-stop path to verify")
	}
}

func TestStopPolicyStopsBeforeReferencedNode(t *testing.T) {
	path := mockPath(
		newMockMPTNode("n1", "one"),
		newMockMPTNode("n2", "two"),
		newMockMPTNode("n3", "latest-root-reference"),
		newMockMPTNode("n4", "four"),
		newMockMPTNode("leaf", "five"),
	)
	referenced := latestRootReferencedHashes(path[2])
	opts := testOptionsForPaths(t, path)
	opts.Stop = referenced.StopFunc()

	node, err := CompressPathToAggregatedNode(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	if node.NodeCount != 2 {
		t.Fatalf("NodeCount = %d, want 2", node.NodeCount)
	}
	if node.Next == nil {
		t.Fatal("expected Next to point at n3")
	}
	if !bytes.Equal(node.NextHash, path[2].Hash()) {
		t.Fatalf("NextHash = %x, want %x", node.NextHash, path[2].Hash())
	}
	ok, err := VerifyAggregatedNode(node, path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected stopped aggregation to verify")
	}

	oldCommitment := node.Commitment
	node.Next = newMockMPTNode("n3", "mutated-latest-root-reference")
	if !node.Commitment.Equal(&oldCommitment) {
		t.Fatal("mutating Next changed current commitment")
	}
	ok, err = VerifyAggregatedNode(node, path)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("mutated Next passed NextHash verification")
	}
}

func TestStopPolicyFirstNodeReturnsError(t *testing.T) {
	path := mockPath(
		newMockMPTNode("n1", "latest-root-reference"),
		newMockMPTNode("n2", "two"),
	)
	referenced := latestRootReferencedHashes(path[0])
	opts := testOptionsForPaths(t, path)
	opts.Stop = referenced.StopFunc()

	if _, err := CompressPathToAggregatedNode(path, opts); err == nil {
		t.Fatal("expected error when stop policy matches first node")
	}
}

func TestStopPolicyAppliesPerBranch(t *testing.T) {
	branches := [][]MPTNodeLike{
		mockPath(newMockMPTNode("a1", "one"), newMockMPTNode("a2", "stop"), newMockMPTNode("a3", "three")),
		mockPath(newMockMPTNode("b1", "one"), newMockMPTNode("b2", "two"), newMockMPTNode("b3", "stop")),
		mockPath(newMockMPTNode("c1", "one"), newMockMPTNode("c2", "two")),
	}
	referenced := latestRootReferencedHashes(branches[0][1], branches[1][2])
	opts := testOptionsForPaths(t, branches...)
	opts.Stop = referenced.StopFunc()

	nodes, err := CompressBranches(branches, opts)
	if err != nil {
		t.Fatal(err)
	}
	wantCounts := []int{1, 2, 2}
	for i := range nodes {
		if nodes[i].NodeCount != wantCounts[i] {
			t.Fatalf("branch %d NodeCount = %d, want %d", i, nodes[i].NodeCount, wantCounts[i])
		}
		ok, err := VerifyAggregatedNode(nodes[i], branches[i])
		if err != nil {
			t.Fatalf("verify branch %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("branch %d failed verification", i)
		}
	}
	if !bytes.Equal(nodes[0].NextHash, branches[0][1].Hash()) {
		t.Fatal("branch 0 NextHash mismatch")
	}
	if !bytes.Equal(nodes[1].NextHash, branches[1][2].Hash()) {
		t.Fatal("branch 1 NextHash mismatch")
	}
	if nodes[2].Next != nil {
		t.Fatal("branch 2 should not have Next")
	}
}

func TestAppendTail(t *testing.T) {
	path := mockPath(
		newMockMPTNode("n1", "one"),
		newMockMPTNode("n2", "two"),
		newMockMPTNode("n3", "three"),
	)
	opts := testOptionsForPaths(t, path)
	node := mustCompress(t, opts, path[:2])
	oldCommitment := node.Commitment

	appended, err := AppendTail(node, path[2])
	if err != nil {
		t.Fatal(err)
	}
	if appended.NodeCount != 3 {
		t.Fatalf("NodeCount = %d, want 3", appended.NodeCount)
	}
	if len(appended.OriginalNodeHashes) != 3 || !bytes.Equal(appended.OriginalNodeHashes[2], path[2].Hash()) {
		t.Fatal("tail hash was not recorded")
	}
	if appended.Commitment.Equal(&oldCommitment) {
		t.Fatal("commitment did not change after append")
	}
	ok, err := VerifyAggregatedNode(appended, path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("appended node did not verify")
	}
}

func TestRemoveTail(t *testing.T) {
	path := mockPath(
		newMockMPTNode("n1", "one"),
		newMockMPTNode("n2", "two"),
		newMockMPTNode("n3", "three"),
	)
	opts := testOptionsForPaths(t, path)
	node := mustCompress(t, opts, path)
	oldCommitment := node.Commitment

	removedNode, removed, err := RemoveTail(node)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(removed.Hash(), path[2].Hash()) {
		t.Fatal("removed node mismatch")
	}
	if removedNode.NodeCount != 2 {
		t.Fatalf("NodeCount = %d, want 2", removedNode.NodeCount)
	}
	if removedNode.Commitment.Equal(&oldCommitment) {
		t.Fatal("commitment did not change after remove")
	}
	ok, err := VerifyAggregatedNode(removedNode, path[:2])
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("removed-tail node did not verify against shortened path")
	}
	ok, err = VerifyAggregatedNode(removedNode, path)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("removed-tail node verified against old full path")
	}
}

func TestPathCommitmentOrderSensitive(t *testing.T) {
	path := mockPath(
		newMockMPTNode("n1", "one"),
		newMockMPTNode("n2", "two"),
		newMockMPTNode("n3", "three"),
	)
	reordered := mockPath(
		newMockMPTNode("n1", "one"),
		newMockMPTNode("n3", "three"),
		newMockMPTNode("n2", "two"),
	)
	opts := testOptionsForPaths(t, path, reordered)

	left := mustCompress(t, opts, path)
	right := mustCompress(t, opts, reordered)
	if left.Commitment.Equal(&right.Commitment) {
		t.Fatal("commitment ignored root-to-leaf node order")
	}
}

func TestTailUpdateNextBoundary(t *testing.T) {
	path := mockPath(
		newMockMPTNode("n1", "one"),
		newMockMPTNode("n2", "two"),
		newMockMPTNode("n3", "stop"),
		newMockMPTNode("n4", "four"),
	)
	referenced := latestRootReferencedHashes(path[2])
	opts := testOptionsForPaths(t, path)
	opts.Stop = referenced.StopFunc()
	node := mustCompress(t, opts, path)

	if _, err := AppendTail(node, path[3]); err == nil {
		t.Fatal("expected append across Next boundary to fail")
	}
	removedNode, _, err := RemoveTail(node)
	if err != nil {
		t.Fatal(err)
	}
	if removedNode.Next == nil || !bytes.Equal(removedNode.NextHash, path[2].Hash()) {
		t.Fatal("RemoveTail changed Next boundary")
	}
	updatedPath := []MPTNodeLike{path[0], path[2]}
	ok, err := VerifyAggregatedNode(removedNode, updatedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("removed-tail node with Next did not verify")
	}
}

func TestAggregatedViewDoesNotRequireDatabase(t *testing.T) {
	branches := [][]MPTNodeLike{
		mockPath(newMockMPTNode("a", "one")),
		mockPath(newMockMPTNode("b", "two"), newMockMPTNode("bb", "three")),
	}
	opts := testOptionsForPaths(t, branches...)

	view, err := NewAggregatedView([]byte("cut"), branches, opts)
	if err != nil {
		t.Fatal(err)
	}
	if string(view.CutPoint) != "cut" {
		t.Fatalf("cut point = %q", view.CutPoint)
	}
	if len(view.Children) != len(branches) {
		t.Fatalf("view children = %d, want %d", len(view.Children), len(branches))
	}
}

func TestAggregatedMPTVerbose(t *testing.T) {
	n1 := newMockMPTNode("n1", "old-root")
	n2 := newMockMPTNode("n2", "old-only")
	n3 := newMockMPTNode("n3", "shared")
	n4 := newMockMPTNode("n4", "old-leaf")
	n5 := newMockMPTNode("n5", "shared-leaf")
	n6 := newMockMPTNode("n6", "latest-root")
	n7 := newMockMPTNode("n7", "latest-only")
	n8 := newMockMPTNode("n8", "latest-leaf")

	allPaths := [][]MPTNodeLike{
		mockPath(n1, n2, n4),
		mockPath(n1, n3, n5),
		mockPath(n6, n5),
		mockPath(n6, n7, n8),
	}
	pruneBranches := allPaths[:2]
	latestRootPaths := allPaths[2:]
	referenced := latestRootReferencedHashes(n6, n5, n7, n8)
	opts := testOptionsForPaths(t, allPaths...)
	opts.Stop = referenced.StopFunc()

	t.Logf("mock multi-version MPT before pruning:")
	t.Logf("  old root: n1")
	for i, branch := range pruneBranches {
		t.Logf("    oldPath[%d]: %s", i, describePath(t, branch))
	}
	t.Logf("  latest root: n6")
	for i, branch := range latestRootPaths {
		t.Logf("    latestPath[%d]: %s", i, describePath(t, branch))
	}
	t.Logf("latest root referenced hash set:")
	for hash := range referenced {
		t.Logf("  %x", []byte(hash))
	}
	t.Logf("compression rule: only old root n1 paths are pruned; each path stops before the first latest-root referenced node")

	for i, branch := range pruneBranches {
		logBranchCompression(t, opts, referenced, i, branch)
	}

	nodes, err := CompressBranches(pruneBranches, opts)
	if err != nil {
		t.Fatal(err)
	}
	view := AggregatedView{
		CutPoint: []byte("n1"),
		Children: nodes,
	}
	t.Logf("old root n1 after in-memory aggregation:")
	t.Logf("  root(%s)", view.CutPoint)
	for i := range nodes {
		next := "<nil>"
		if nodes[i].Next != nil {
			next = string(nodes[i].Next.PathFragment())
		}
		t.Logf("  |-- AggregatedNode[%d]: compressed=%s next=%s nodeCount=%d hashes=%s commitment=%s",
			i, describePath(t, nodes[i].Nodes), next, nodes[i].NodeCount, formatHashes(nodes[i].OriginalNodeHashes), formatG1(nodes[i].Commitment))
	}
	t.Logf("latest root n6 remains unmodified:")
	for i, branch := range latestRootPaths {
		t.Logf("  latestPath[%d]: %s", i, describePath(t, branch))
	}
	logProcessedMPT(t, view, latestRootPaths)

	tailNode := newMockMPTNode("n9", "old-tail")
	tailUpdatePath := append(clonePath(pruneBranches[0]), tailNode)
	base := mustCompress(t, testOptionsForPaths(t, pruneBranches[0], tailUpdatePath), pruneBranches[0])
	t.Logf("tail update before append: nodeCount=%d hashes=%s commitment=%s", base.NodeCount, formatHashes(base.OriginalNodeHashes), formatG1(base.Commitment))
	appended, err := AppendTail(base, tailNode)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tail update after append: nodeCount=%d hashes=%s commitment=%s", appended.NodeCount, formatHashes(appended.OriginalNodeHashes), formatG1(appended.Commitment))
	removed, tail, err := RemoveTail(appended)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tail update after remove: removed=%s nodeCount=%d hashes=%s commitment=%s", tail.PathFragment(), removed.NodeCount, formatHashes(removed.OriginalNodeHashes), formatG1(removed.Commitment))
	ok, err := VerifyAggregatedNode(removed, pruneBranches[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tail update verification result: %v", ok)
}

func mockPath(nodes ...mockMPTNode) []MPTNodeLike {
	out := make([]MPTNodeLike, len(nodes))
	for i := range nodes {
		out[i] = nodes[i]
	}
	return out
}

func clonePath(path []MPTNodeLike) []MPTNodeLike {
	out := make([]MPTNodeLike, len(path))
	copy(out, path)
	return out
}

func appendLengthPrefixed(out, data []byte) []byte {
	var prefix [8]byte
	binary.BigEndian.PutUint64(prefix[:], uint64(len(data)))
	out = append(out, prefix[:]...)
	return append(out, data...)
}

type referencedHashSet map[string]bool

func latestRootReferencedHashes(nodes ...MPTNodeLike) referencedHashSet {
	out := make(referencedHashSet)
	for _, node := range nodes {
		out[string(node.Hash())] = true
	}
	return out
}

func (s referencedHashSet) StopFunc() StopFunc {
	return func(node MPTNodeLike) bool {
		return s[string(node.Hash())]
	}
}

func logBranchCompression(t *testing.T, opts CompressOptions, referenced referencedHashSet, branchIndex int, branch []MPTNodeLike) {
	t.Helper()

	t.Logf("branch[%d] original path: %s", branchIndex, describePath(t, branch))
	for i, node := range branch {
		encoded, err := node.Encode()
		if err != nil {
			t.Fatal(err)
		}
		stop := referenced[string(node.Hash())]
		t.Logf("branch[%d] visit node[%d] path=%s hash=%x stopBefore=%v encoded=%x", branchIndex, i, node.PathFragment(), node.Hash(), stop, encoded)
	}
	aggregated, err := CompressPathToAggregatedNode(branch, opts)
	if err != nil {
		t.Fatal(err)
	}
	next := "<nil>"
	if aggregated.Next != nil {
		next = string(aggregated.Next.PathFragment())
	}
	t.Logf("branch[%d] compressed node list: %s", branchIndex, describePath(t, aggregated.Nodes))
	t.Logf("branch[%d] Next node: %s hash=%x", branchIndex, next, aggregated.NextHash)
	t.Logf("branch[%d] commitment coverage: AggregatedNode.Nodes only; Next is not included", branchIndex)
	canonical, err := CanonicalPathBytes(aggregated.Nodes)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("branch[%d] canonical bytes: %x", branchIndex, canonical)
	chunks, err := BytesToFrChunks(canonical)
	if err != nil {
		t.Fatal(err)
	}
	for i := range chunks {
		start := i * frChunkSize
		end := start + frChunkSize
		if end > len(canonical) {
			end = len(canonical)
		}
		t.Logf("branch[%d] chunk[%d] bytes=%x fr=%s", branchIndex, i, canonical[start:end], formatFr(chunks[i]))
	}
	commitment, elements, err := CommitPath(opts.Params, aggregated.Nodes)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("branch[%d] fr.Element vector after padding: %s", branchIndex, formatFrVector(elements))
	t.Logf("branch[%d] path commitment: %s", branchIndex, formatG1(commitment))
	t.Logf("branch[%d] AggregatedNode: nodeCount=%d hashes=%s chunkCount=%d elementCount=%d commitment=%s",
		branchIndex, aggregated.NodeCount, formatHashes(aggregated.OriginalNodeHashes), aggregated.ChunkCount, aggregated.ElementCount, formatG1(aggregated.Commitment))
	ok, err := VerifyAggregatedNode(aggregated, branch)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("branch[%d] VerifyAggregatedNode result: %v", branchIndex, ok)
}

func logProcessedMPT(t *testing.T, view AggregatedView, latestRootPaths [][]MPTNodeLike) {
	t.Helper()

	t.Logf("processed MPT tree:")
	t.Logf("%s (old root, aggregated view)", view.CutPoint)
	for i, node := range view.Children {
		t.Logf("  AggregatedNode[%d]", i)
		t.Logf("    covers: %s", describePath(t, node.Nodes))
		t.Logf("    commitment: %s", formatG1(node.Commitment))
		if node.Next != nil {
			t.Logf("    Next")
			t.Logf("      %s hash=%x", node.Next.PathFragment(), node.NextHash)
		} else {
			t.Logf("    Next: <nil>")
		}
	}
	logExpandedLatestRoot(t, latestRootPaths)
}

func logExpandedLatestRoot(t *testing.T, paths [][]MPTNodeLike) {
	t.Helper()
	if len(paths) == 0 || len(paths[0]) == 0 {
		return
	}
	root := string(paths[0][0].PathFragment())
	t.Logf("%s (latest root, unchanged)", root)
	seen := make(map[string]bool)
	for _, path := range paths {
		for depth := 1; depth < len(path); depth++ {
			key := pathPrefixKey(path[:depth+1])
			if seen[key] {
				continue
			}
			seen[key] = true
			t.Logf("%s%s", indent(depth), path[depth].PathFragment())
		}
	}
}

func pathPrefixKey(path []MPTNodeLike) string {
	out := ""
	for i, node := range path {
		if i > 0 {
			out += "/"
		}
		out += string(node.PathFragment())
	}
	return out
}

func indent(depth int) string {
	out := ""
	for i := 0; i < depth; i++ {
		out += "  "
	}
	return out
}

func testOptionsForPaths(t *testing.T, paths ...[]MPTNodeLike) CompressOptions {
	t.Helper()
	maxLen := 1
	for _, path := range paths {
		canonical, err := CanonicalPathBytes(path)
		if err != nil {
			t.Fatal(err)
		}
		chunks, err := BytesToFrChunks(canonical)
		if err != nil {
			t.Fatal(err)
		}
		if len(chunks) > maxLen {
			maxLen = len(chunks)
		}
		withPrefixZeros := len(chunks) + len(path)
		if withPrefixZeros > maxLen {
			maxLen = withPrefixZeros
		}
	}
	params, err := ipa.NewTestParams(nextPowerOfTwo(maxLen))
	if err != nil {
		t.Fatal(err)
	}
	return CompressOptions{Params: params}
}

func mustCompress(t *testing.T, opts CompressOptions, path []MPTNodeLike) *AggregatedNode {
	t.Helper()
	node, err := CompressPathToAggregatedNode(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func nextPowerOfTwo(n int) int {
	out := 1
	for out < n {
		out <<= 1
	}
	return out
}

func describePath(t *testing.T, path []MPTNodeLike) string {
	t.Helper()
	if len(path) == 0 {
		return "<empty>"
	}
	out := ""
	for i, node := range path {
		if i > 0 {
			out += " -> "
		}
		out += string(node.PathFragment())
	}
	return out
}

func formatG1(p bn254.G1Affine) string {
	return fmt.Sprintf("(%s,%s)", p.X.String(), p.Y.String())
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

func formatHashes(hashes [][]byte) string {
	out := "["
	for i := range hashes {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%x", hashes[i])
	}
	return out + "]"
}
