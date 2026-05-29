package mptagg

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/fnv"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/ethereum/go-ethereum/internal/partitioning"
)

type LocalViewNodeKind int

const (
	RawMPTNode LocalViewNodeKind = iota
	AggregatedCommitmentNode
)

type LocalViewNode struct {
	ID   string
	Kind LocalViewNodeKind

	PathKey []byte

	RawNode    *partitioning.PathNode
	RawReason  string
	Aggregated *AggregatedNode

	Children []*LocalViewNode
}

type LocalPartitionView struct {
	PartitionID int

	Nodes []*LocalViewNode
	Edges []LocalViewEdge

	RawPathKeys              [][]byte
	PreservedLatestPathKeys  [][]byte
	RawSuperNodes            []partitioning.SuperNode
	RawAvailableHashes       map[string]bool
	CandidateAggregatedNodes []*AggregatedNode
	AggregatedNodes          []*AggregatedNode
	MergeEvents              []AggregatedMergeEvent
}

type LocalViewEdge struct {
	FromID  string
	ToID    string
	PathKey []byte
	Kind    string
}

type AggregatedMergeEvent struct {
	Key       string
	FromID    string
	IntoID    string
	NextCount int
}

// MPTStatePathRoot is the root handle surface consumed by mptagg. Its
// ExtractStatePaths implementation should DFS the MPT root and emit complete
// root-to-leaf StatePaths.
type MPTStatePathRoot interface {
	RootHashString() string
	ExtractStatePaths() ([]partitioning.StatePath, error)
}

// OwnedRawNodeSet tracks raw MPT node hashes already present in one local
// partition across multiple partitioning rounds.
type OwnedRawNodeSet struct {
	hashes map[string]bool
}

func NewOwnedRawNodeSet() *OwnedRawNodeSet {
	return &OwnedRawNodeSet{hashes: make(map[string]bool)}
}

func (s *OwnedRawNodeSet) Add(hash string) {
	if s == nil {
		return
	}
	if s.hashes == nil {
		s.hashes = make(map[string]bool)
	}
	s.hashes[normalizeRawHashKey(hash)] = true
}

func (s *OwnedRawNodeSet) AddSet(rawNodeSet map[string]bool) {
	if s == nil {
		return
	}
	for hash, owned := range rawNodeSet {
		if owned {
			s.Add(hash)
		}
	}
}

func (s *OwnedRawNodeSet) Contains(hash string) bool {
	return s != nil && s.hashes[normalizeRawHashKey(hash)]
}

func (s *OwnedRawNodeSet) Snapshot() map[string]bool {
	if s == nil {
		return nil
	}
	out := make(map[string]bool, len(s.hashes))
	for hash, owned := range s.hashes {
		out[hash] = owned
	}
	return out
}

// BuildLocalPartitionViewFromRootRawNodeSet builds a local Missing-Path
// Commitment view from a root and the raw node hash set assigned to one
// partition in the current round. The root is DFSed by mptagg, and local
// ownership is the union of previously owned hashes in owned plus this round's
// rawNodeSet. On success, rawNodeSet is added to owned for future rounds.
func BuildLocalPartitionViewFromRootRawNodeSet(root MPTStatePathRoot, primaryPaths []partitioning.StatePath, rawNodeSet map[string]bool, owned *OwnedRawNodeSet, partitionID int, opts CompressOptions) (*LocalPartitionView, error) {
	if root == nil {
		return nil, fmt.Errorf("nil MPT root")
	}
	paths, err := root.ExtractStatePaths()
	if err != nil {
		return nil, fmt.Errorf("extract StatePaths from root %q: %w", root.RootHashString(), err)
	}
	localOwned := mergeOwnedRawNodeSets(owned, rawNodeSet)
	view, err := buildLocalPartitionViewFromOwnedRawNodeSet(paths, primaryPaths, localOwned, partitionID, opts)
	if err != nil {
		return nil, err
	}
	if owned != nil {
		owned.AddSet(rawNodeSet)
	}
	return view, nil
}

func mergeOwnedRawNodeSets(owned *OwnedRawNodeSet, rawNodeSet map[string]bool) map[string]bool {
	out := owned.Snapshot()
	if out == nil {
		out = make(map[string]bool, len(rawNodeSet))
	}
	for hash, keepRaw := range rawNodeSet {
		if keepRaw {
			out[normalizeRawHashKey(hash)] = true
		}
	}
	return out
}

func buildLocalPartitionViewFromOwnedRawNodeSet(paths []partitioning.StatePath, primaryPaths []partitioning.StatePath, rawNodeSet map[string]bool, partitionID int, opts CompressOptions) (*LocalPartitionView, error) {
	if partitionID < 0 {
		return nil, fmt.Errorf("negative partitionID %d", partitionID)
	}
	if opts.Params == nil {
		return nil, fmt.Errorf("nil commitment params")
	}
	primary := make(map[string]bool, len(primaryPaths))
	for i := range primaryPaths {
		primary[PathID(primaryPaths[i])] = true
	}
	view := &LocalPartitionView{
		PartitionID:              partitionID,
		Nodes:                    []*LocalViewNode{},
		Edges:                    []LocalViewEdge{},
		CandidateAggregatedNodes: []*AggregatedNode{},
		AggregatedNodes:          []*AggregatedNode{},
		MergeEvents:              []AggregatedMergeEvent{},
		RawAvailableHashes:       rawAvailableHashMapFromNodeHashSet(rawNodeSet),
	}
	for i := range primaryPaths {
		view.RawPathKeys = append(view.RawPathKeys, append([]byte(nil), primaryPaths[i].Key...))
	}
	for i := range paths {
		path := paths[i]
		if len(path.Nodes) == 0 {
			continue
		}
		if pathFullyOwnedByRawNodeSet(path, rawNodeSet) {
			nodes := buildRawPathNodesFromOwnedRawNodeSet(path, primary[PathID(path)])
			view.Nodes = append(view.Nodes, nodes...)
			appendPrimaryRawEdges(view, path)
			continue
		}
		agg, err := buildLocalAggregatedNode(path, clonePartitioningPathNodes(path.Nodes), 0, len(path.Nodes), rawSetOnlyCompressOptions(opts))
		if err != nil {
			return nil, err
		}
		agg.AttachedRawNodes = attachedRawNodesFromOwnedRawNodeSet(path, rawNodeSet)
		view.CandidateAggregatedNodes = append(view.CandidateAggregatedNodes, agg)
		view.AggregatedNodes = append(view.AggregatedNodes, agg)
		view.Nodes = append(view.Nodes, &LocalViewNode{
			ID:         agg.ID,
			Kind:       AggregatedCommitmentNode,
			PathKey:    append([]byte(nil), path.Key...),
			Aggregated: agg,
		})
	}
	return view, nil
}

func pathFullyOwnedByRawNodeSet(path partitioning.StatePath, rawSet map[string]bool) bool {
	if len(path.Nodes) == 0 {
		return true
	}
	for i := range path.Nodes {
		if !rawSetHasNode(rawSet, path.Nodes[i]) {
			return false
		}
	}
	return true
}

func buildRawPathNodesFromOwnedRawNodeSet(path partitioning.StatePath, primary bool) []*LocalViewNode {
	out := make([]*LocalViewNode, 0, len(path.Nodes))
	reason := "owned-raw-set"
	if primary {
		reason = "primary-owned-path"
	}
	for i := range path.Nodes {
		node := clonePartitioningPathNode(path.Nodes[i])
		out = append(out, &LocalViewNode{
			ID:        rawNodeID(node.Hash),
			Kind:      RawMPTNode,
			PathKey:   append([]byte(nil), path.Key...),
			RawNode:   &node,
			RawReason: reason,
		})
	}
	return out
}

func attachedRawNodesFromOwnedRawNodeSet(path partitioning.StatePath, rawSet map[string]bool) []AttachedRawNode {
	out := make([]AttachedRawNode, 0)
	for i := range path.Nodes {
		if !rawSetHasNode(rawSet, path.Nodes[i]) {
			continue
		}
		out = append(out, AttachedRawNode{
			Hash:     append([]byte(nil), path.Nodes[i].Hash...),
			Position: path.Nodes[i].Position,
			PathKey:  append([]byte(nil), path.Key...),
			NodeID:   rawNodeID(path.Nodes[i].Hash),
			Reason:   "owned-raw-set",
		})
	}
	return out
}

func rawAvailableHashMapFromNodeHashSet(rawNodeSet map[string]bool) map[string]bool {
	out := make(map[string]bool, len(rawNodeSet))
	for hash, keepRaw := range rawNodeSet {
		if keepRaw {
			out[normalizeRawHashKey(hash)] = true
		}
	}
	return out
}

func rawSetHasNode(rawSet map[string]bool, node partitioning.PathNode) bool {
	return rawSet[normalizeRawHashKey(hex.EncodeToString(node.Hash))]
}

func normalizeRawHashKey(hash string) string {
	if len(hash) >= 2 && hash[:2] == "0x" {
		return hash[2:]
	}
	return hash
}

// BuildLocalPartitionViewFromRawNodeSet builds the current mptagg mainline view
// from complete StatePaths and the raw node hashes owned by one partition.
//
// If every node hash on a path is in rawSet, the full path is emitted as raw.
// Otherwise the whole path is represented by one AggregatedNode commitment, and
// any owned raw nodes on that path are attached under that commitment.
func BuildLocalPartitionViewFromRawNodeSet(paths []partitioning.StatePath, rawSet map[string]bool, partitionID int, opts CompressOptions) (*LocalPartitionView, error) {
	if partitionID < 0 {
		return nil, fmt.Errorf("negative partitionID %d", partitionID)
	}
	if opts.Params == nil {
		return nil, fmt.Errorf("nil commitment params")
	}
	return buildLocalPartitionViewFromOwnedRawNodeSet(paths, nil, rawAvailableHashMapFromNodeHashSet(rawSet), partitionID, opts)
}

func rawSetOnlyCompressOptions(opts CompressOptions) CompressOptions {
	return CompressOptions{Params: opts.Params}
}

func appendPrimaryRawEdges(view *LocalPartitionView, path partitioning.StatePath) {
	for i := 1; i < len(path.Nodes); i++ {
		view.Edges = append(view.Edges, LocalViewEdge{
			FromID:  rawNodeID(path.Nodes[i-1].Hash),
			ToID:    rawNodeID(path.Nodes[i].Hash),
			PathKey: append([]byte(nil), path.Key...),
			Kind:    "raw",
		})
	}
}

func PathID(path partitioning.StatePath) string {
	var buf bytes.Buffer
	buf.WriteString("key:")
	buf.WriteString(hex.EncodeToString(path.Key))
	buf.WriteString("|leaf:")
	buf.WriteString(hex.EncodeToString(path.LeafHash))
	buf.WriteString("|nodes:")
	for i := range path.Nodes {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(hex.EncodeToString(path.Nodes[i].Hash))
	}
	return buf.String()
}

func buildLocalAggregatedNode(path partitioning.StatePath, segment []partitioning.PathNode, startIndex, nextIndex int, opts CompressOptions) (*AggregatedNode, error) {
	if len(segment) == 0 {
		return nil, fmt.Errorf("empty aggregated segment")
	}
	hashes := make([][]byte, len(segment))
	for i := range segment {
		hashes[i] = append([]byte(nil), segment[i].Hash...)
	}
	adapted := adaptPathNodes(segment)
	agg, err := CompressPathToAggregatedNode(adapted, opts)
	if err != nil {
		return nil, err
	}
	commitment, elements, prefixZeros, err := CommitPathWithPositionPrefix(opts.Params, adapted, segment[0].Position)
	if err != nil {
		return nil, err
	}
	agg.PathKey = append([]byte(nil), path.Key...)
	agg.Nibbles = localSegmentNibbles(path, startIndex, nextIndex)
	agg.OriginalNodeHashes = cloneBytes2D(hashes)
	agg.CommitmentPrefixZeros = prefixZeros
	agg.Commitment = commitment
	agg.ElementCount = len(elements)
	agg.NextPosition = -1
	agg.ID = aggregatedNodeID(aggregatedNodeKey(agg))
	return agg, nil
}

func aggregatedNodeKey(agg *AggregatedNode) string {
	var buf bytes.Buffer
	buf.WriteString("path:")
	buf.WriteString(hex.EncodeToString(agg.PathKey))
	buf.WriteByte('|')
	buf.WriteString("nibbles:")
	buf.WriteString(hex.EncodeToString(agg.Nibbles))
	buf.WriteString("|hashes:")
	for i := range agg.OriginalNodeHashes {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(hex.EncodeToString(agg.OriginalNodeHashes[i]))
	}
	buf.WriteString("|commit:")
	buf.WriteString(formatG1Local(agg.Commitment))
	return buf.String()
}

func localSegmentNibbles(path partitioning.StatePath, start, end int) []byte {
	if end <= start {
		return nil
	}
	if pathHasTrieFragments(path) {
		var out bytes.Buffer
		for i := start; i < end && i < len(path.Nodes); i++ {
			writeNodePathNibbles(&out, path.Nodes[i])
		}
		return out.Bytes()
	}
	var buf bytes.Buffer
	for i := start; i < end && i < len(path.Nodes); i++ {
		buf.Write(path.Nodes[i].Hash)
		buf.WriteByte('/')
	}
	return buf.Bytes()
}

func writeNodePathNibbles(out *bytes.Buffer, node partitioning.PathNode) {
	if node.EdgeNibble != 0xff {
		out.WriteByte(node.EdgeNibble)
	}
	out.Write(node.PrefixNibbles)
}

func pathHasTrieFragments(path partitioning.StatePath) bool {
	for i := range path.Nodes {
		if path.Nodes[i].NodeType != "" || len(path.Nodes[i].PrefixNibbles) > 0 {
			return true
		}
	}
	return false
}

func rawNodeID(hash []byte) string {
	return "raw-" + hex.EncodeToString(hash)
}

func aggregatedNodeID(key string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return fmt.Sprintf("agg-%016x", h.Sum64())
}

func formatG1Local(p bn254.G1Affine) string {
	return fmt.Sprintf("(%s,%s)", p.X.String(), p.Y.String())
}

// CommitAggregatedSegment returns a deterministic in-memory segment commitment
// for a full PathNode slice through the position-aware local-view backend. It
// is deliberately not a proof API.
func CommitAggregatedSegment(params *CommitmentParams, nodes []partitioning.PathNode) (AggregatedNode, error) {
	if len(nodes) == 0 {
		return AggregatedNode{}, fmt.Errorf("empty segment")
	}
	agg, err := CompressPathToAggregatedNode(adaptPathNodes(nodes), CompressOptions{Params: params})
	if err != nil {
		return AggregatedNode{}, err
	}
	return *agg, nil
}

func CommitAggregatedStatePathSegment(params *CommitmentParams, path partitioning.StatePath, startPosition int, endPosition int) (AggregatedNode, error) {
	if len(path.Nodes) == 0 {
		return AggregatedNode{}, fmt.Errorf("empty StatePath")
	}
	if startPosition < 0 || endPosition < startPosition || endPosition >= len(path.Nodes) {
		return AggregatedNode{}, fmt.Errorf("invalid segment positions [%d,%d] for path length %d", startPosition, endPosition, len(path.Nodes))
	}
	segment := adaptPathNodes(path.Nodes[startPosition : endPosition+1])
	commitment, elements, prefixZeros, err := CommitPathWithPositionPrefix(params, segment, startPosition)
	if err != nil {
		return AggregatedNode{}, err
	}
	hashes := make([][]byte, 0, endPosition-startPosition+1)
	for pos := startPosition; pos <= endPosition; pos++ {
		hashes = append(hashes, append([]byte(nil), path.Nodes[pos].Hash...))
	}
	return AggregatedNode{
		PathKey:               append([]byte(nil), path.Key...),
		CommitmentPrefixZeros: prefixZeros,
		NodeCount:             endPosition - startPosition + 1,
		OriginalNodeHashes:    cloneBytes2D(hashes),
		Commitment:            commitment,
		ElementCount:          len(elements),
		Nodes:                 segment,
		params:                params,
	}, nil
}

type pathNodeAdapter struct {
	node partitioning.PathNode
}

func (a pathNodeAdapter) Encode() ([]byte, error) {
	var buf bytes.Buffer
	var prefix [8]byte
	binary.BigEndian.PutUint64(prefix[:], uint64(len(a.node.PathKey)))
	buf.Write(prefix[:])
	buf.Write(a.node.PathKey)
	binary.BigEndian.PutUint64(prefix[:], uint64(len(a.node.NodeType)))
	buf.Write(prefix[:])
	buf.WriteString(a.node.NodeType)
	buf.WriteByte(a.node.EdgeNibble)
	binary.BigEndian.PutUint64(prefix[:], uint64(len(a.node.PrefixNibbles)))
	buf.Write(prefix[:])
	buf.Write(a.node.PrefixNibbles)
	binary.BigEndian.PutUint64(prefix[:], uint64(a.node.Position))
	buf.Write(prefix[:])
	binary.BigEndian.PutUint64(prefix[:], uint64(a.node.ParentPosition+1))
	buf.Write(prefix[:])
	binary.BigEndian.PutUint64(prefix[:], uint64(a.node.ChildCount))
	buf.Write(prefix[:])
	binary.BigEndian.PutUint64(prefix[:], uint64(len(a.node.Hash)))
	buf.Write(prefix[:])
	buf.Write(a.node.Hash)
	return buf.Bytes(), nil
}

func (a pathNodeAdapter) Hash() []byte {
	return append([]byte(nil), a.node.Hash...)
}

func (a pathNodeAdapter) PathFragment() []byte {
	if a.node.NodeType != "" || len(a.node.PrefixNibbles) > 0 {
		var buf bytes.Buffer
		buf.WriteString(a.node.NodeType)
		buf.WriteByte(':')
		buf.Write(a.node.PrefixNibbles)
		buf.WriteByte(':')
		buf.WriteByte(a.node.EdgeNibble)
		return buf.Bytes()
	}
	var buf bytes.Buffer
	buf.Write(a.node.PathKey)
	buf.WriteByte(':')
	var pos [8]byte
	binary.BigEndian.PutUint64(pos[:], uint64(a.node.Position))
	buf.Write(pos[:])
	return buf.Bytes()
}

func adaptPathNodes(nodes []partitioning.PathNode) []MPTNodeLike {
	out := make([]MPTNodeLike, len(nodes))
	for i := range nodes {
		out[i] = pathNodeAdapter{node: clonePartitioningPathNode(nodes[i])}
	}
	return out
}

func clonePartitioningPathNodes(nodes []partitioning.PathNode) []partitioning.PathNode {
	out := make([]partitioning.PathNode, len(nodes))
	for i := range nodes {
		out[i] = clonePartitioningPathNode(nodes[i])
	}
	return out
}

func clonePartitioningPathNode(node partitioning.PathNode) partitioning.PathNode {
	return partitioning.PathNode{
		Hash:           append([]byte(nil), node.Hash...),
		PathKey:        append([]byte(nil), node.PathKey...),
		NodeType:       node.NodeType,
		EdgeNibble:     node.EdgeNibble,
		PrefixNibbles:  append([]byte(nil), node.PrefixNibbles...),
		Position:       node.Position,
		ParentPosition: node.ParentPosition,
		ChildCount:     node.ChildCount,
		Size:           node.Size,
		Raw:            node.Raw,
	}
}

func cloneBytes2D(in [][]byte) [][]byte {
	out := make([][]byte, len(in))
	for i := range in {
		out[i] = append([]byte(nil), in[i]...)
	}
	return out
}
