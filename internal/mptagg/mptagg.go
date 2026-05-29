package mptagg

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/internal/fileipa"
	"github.com/ethereum/go-ethereum/internal/ipa"
)

const frChunkSize = 31

// CommitmentParams are the project vector-commitment parameters used by this
// in-memory aggregated MPT view.
type CommitmentParams = ipa.Params

// StopFunc returns true when path compression must stop before node.
// StopFunc 判断某个节点是否仍被最新 root 引用；如果返回 true，则压缩在该节点之前停止。
type StopFunc func(node MPTNodeLike) bool

// CompressOptions configures in-memory path compression.
// CompressOptions 保存压缩配置，包括 commitment 参数和可选的停止策略。
type CompressOptions struct {
	// Params 是生成 path commitment 所需的项目 commitment 参数。
	Params *CommitmentParams
	// Stop 是可选停止策略，用于避免压缩仍被最新 root 引用的节点。
	Stop StopFunc
	// LatestReachable records node hashes reachable from the latest MPT root.
	LatestReachable map[string]bool
	// RespectLatestBoundary stops historical path compression before a
	// latest-reachable node. Latest-version paths themselves are not stopped.
	RespectLatestBoundary bool
	// PathVersions maps StatePath keys to their version height for local views.
	PathVersions map[string]int64
	// LatestHeight is the height of the latest MPT version in the forest.
	LatestHeight int64
	// PreserveLatest keeps latest-version StatePaths raw in every local view.
	// The default is true; set PreserveLatestSet=true and PreserveLatest=false
	// to explicitly disable it.
	PreserveLatest    bool
	PreserveLatestSet bool
}

// MPTNodeLike is the minimal adapter surface needed to build an in-memory
// aggregated MPT view without depending on trie internals or database writes.
// MPTNodeLike 是聚合模块需要的最小节点接口，用于适配 mock MPT 节点或未来的真实 trie 节点。
type MPTNodeLike interface {
	// Encode 返回节点的规范编码字节，后续会进入 commitment。
	Encode() ([]byte, error)
	// Hash 返回原始节点 hash，用于记录和验证路径中的原始节点身份。
	Hash() []byte
	// PathFragment 返回该节点对应的路径片段，用于组成 AggregatedNode.Path。
	PathFragment() []byte
}

// AggregatedNode represents one compressed MPT path under a cut point.
// Its commitment covers only Nodes, not Next.
// AggregatedNode 表示剪枝断点下某一条连续路径压缩后的内存聚合节点。
type AggregatedNode struct {
	// ID is the stable local-view identity of this merged aggregated node.
	ID string
	// PathKey identifies the StatePath this local-view aggregated segment came from.
	PathKey []byte
	// Nibbles is the trie path fragment covered by this compressed segment.
	Nibbles []byte
	// CommitmentPrefixZeros records how many zero fr.Element slots were
	// prepended before committing this segment so its first element starts at
	// the segment's original StatePath position.
	CommitmentPrefixZeros int

	// Path 是被压缩路径上所有节点路径片段的拼接。
	Path []byte
	// NodeCount 是被压缩进该聚合节点的原始 MPT 节点数量。
	NodeCount int
	// OriginalNodeHashes 保存被压缩节点的原始 hash，验证时会逐一比对。
	OriginalNodeHashes [][]byte
	// Commitment 是对被压缩节点序列生成的 path commitment。
	Commitment bn254.G1Affine
	// ChunkCount 是 canonical bytes 切成 31-byte chunks 后的原始 chunk 数。
	ChunkCount int
	// ElementCount 是实际传入 commitment backend 的元素数，包含补齐到 2 的幂后的零元素。
	ElementCount int
	// Next 是触发停止策略的第一个节点；它不参与当前 AggregatedNode 的 commitment。
	Next MPTNodeLike
	// NextHash 记录 Next 的 hash，用于调试、验证和后续挂接。
	NextHash []byte
	// NextPosition records the next raw PathNode position after this compressed segment.
	NextPosition int
	// CommitmentDigest is a deterministic in-memory segment commitment used by
	// local partition views. TODO: replace with the project path commitment
	// backend once local views carry commitment params.
	CommitmentDigest []byte
	// Nodes 保存被聚合的原始节点序列。第一版只做内存测试，所以保留该字段以支持队尾更新。
	Nodes []MPTNodeLike

	// AttachedRawNodes are secondary raw nodes stored by this partition for the
	// same non-primary path. They do not change the commitment coverage.
	AttachedRawNodes []AttachedRawNode

	params *CommitmentParams
}

type AttachedRawNode struct {
	Hash     []byte
	Position int
	PathKey  []byte
	NodeID   string
	Reason   string
}

// AggregatedView is an in-memory pruned MPT view for one cut point.
// AggregatedView 是某个剪枝断点下的纯内存聚合视图，不写盘、不替换原始 trie。
type AggregatedView struct {
	// CutPoint 记录剪枝断点的路径或标识。
	CutPoint []byte
	// Children 是断点下每条分支各自生成的 AggregatedNode。
	Children []*AggregatedNode
}

// CommitPath canonically encodes one MPT path, chunks the bytes into bn254/fr
// elements, pads to the vector-commitment shape, and commits with the existing
// project commitment backend.
// CommitPath 将一条 MPT 路径编码、分块、补齐，并复用项目已有的向量承诺后端生成承诺。
func CommitPath(params *CommitmentParams, nodes []MPTNodeLike) (bn254.G1Affine, []fr.Element, error) {
	canonical, err := CanonicalPathBytes(nodes)
	if err != nil {
		return bn254.G1Affine{}, nil, err
	}
	chunks, err := BytesToFrChunks(canonical)
	if err != nil {
		return bn254.G1Affine{}, nil, err
	}
	elements := padElementsToPowerOfTwo(chunks)
	commitment, err := ipa.CommitB(params, elements)
	if err != nil {
		return bn254.G1Affine{}, nil, fmt.Errorf("commit MPT path with vector commitment backend: %w", err)
	}
	return commitment, elements, nil
}

// CompressPathToAggregatedNode compresses one continuous MPT path into an
// in-memory AggregatedNode. It does not mutate or delete trie nodes.
// CompressPathToAggregatedNode 将一条连续路径压缩成一个 AggregatedNode，只构造内存结果。
func CompressPathToAggregatedNode(path []MPTNodeLike, opts CompressOptions) (*AggregatedNode, error) {
	compressed, next, err := compressedPrefix(path, opts.Stop)
	if err != nil {
		return nil, err
	}
	return buildAggregatedNode(opts.Params, compressed, next)
}

// CompressBranches compresses each branch independently. It intentionally does
// not merge branch commitments.
// CompressBranches 对断点下的多条分支分别压缩，每条分支独立生成一个 AggregatedNode。
func CompressBranches(branches [][]MPTNodeLike, opts CompressOptions) ([]*AggregatedNode, error) {
	nodes := make([]*AggregatedNode, len(branches))
	for i := range branches {
		aggregated, err := CompressPathToAggregatedNode(branches[i], opts)
		if err != nil {
			return nil, fmt.Errorf("compress branch %d: %w", i, err)
		}
		nodes[i] = aggregated
	}
	return nodes, nil
}

// NewAggregatedView creates an in-memory aggregated view at a cut point.
// NewAggregatedView 构造一个剪枝断点的内存聚合视图，内部不会访问数据库。
func NewAggregatedView(cutPoint []byte, branches [][]MPTNodeLike, opts CompressOptions) (*AggregatedView, error) {
	children, err := CompressBranches(branches, opts)
	if err != nil {
		return nil, err
	}
	return &AggregatedView{
		CutPoint: append([]byte(nil), cutPoint...),
		Children: children,
	}, nil
}

// VerifyAggregatedNode recomputes the path commitment and checks metadata
// against originalPath. If node.Next is set, originalPath must contain that
// stop node immediately after the compressed prefix.
// VerifyAggregatedNode 用原始路径重新计算 commitment，并检查节点数量、hash、Next 和分块元数据。
func VerifyAggregatedNode(node *AggregatedNode, originalPath []MPTNodeLike) (bool, error) {
	if node == nil {
		return false, fmt.Errorf("nil aggregated node")
	}
	if node.params == nil {
		return false, fmt.Errorf("missing commitment params")
	}
	if node.NodeCount == 0 {
		return false, nil
	}
	if len(originalPath) < node.NodeCount {
		return false, nil
	}
	compressed := originalPath[:node.NodeCount]
	recomputed, elements, err := CommitPath(node.params, compressed)
	if err != nil {
		return false, err
	}
	if !node.Commitment.Equal(&recomputed) {
		return false, nil
	}
	if node.ElementCount != len(elements) {
		return false, nil
	}
	canonical, err := CanonicalPathBytes(compressed)
	if err != nil {
		return false, err
	}
	if node.ChunkCount != chunkCount(len(canonical)) {
		return false, nil
	}
	if !bytes.Equal(node.Path, pathBytes(compressed)) {
		return false, nil
	}
	if len(node.OriginalNodeHashes) != len(compressed) {
		return false, nil
	}
	for i := range compressed {
		if !bytes.Equal(node.OriginalNodeHashes[i], compressed[i].Hash()) {
			return false, nil
		}
	}
	if len(node.Nodes) != node.NodeCount {
		return false, nil
	}
	for i := range node.Nodes {
		if !bytes.Equal(node.Nodes[i].Hash(), compressed[i].Hash()) {
			return false, nil
		}
	}
	if node.Next == nil {
		return len(node.NextHash) == 0 && len(originalPath) == node.NodeCount, nil
	}
	if len(node.NextHash) == 0 || !bytes.Equal(node.NextHash, node.Next.Hash()) {
		return false, nil
	}
	if len(originalPath) <= node.NodeCount {
		return false, nil
	}
	return bytes.Equal(node.NextHash, originalPath[node.NodeCount].Hash()), nil
}

// AppendTail appends tail to the compressed sequence and recomputes the path
// commitment. It refuses to cross a Next boundary.
// AppendTail 只允许在聚合序列队尾追加节点；如果存在 Next 边界则返回错误。
func AppendTail(node *AggregatedNode, tail MPTNodeLike) (*AggregatedNode, error) {
	if node == nil {
		return nil, fmt.Errorf("nil aggregated node")
	}
	if node.Next != nil {
		return nil, fmt.Errorf("cannot append tail across Next boundary")
	}
	if tail == nil {
		return nil, fmt.Errorf("nil tail node")
	}
	nodes := cloneNodes(node.Nodes)
	nodes = append(nodes, tail)
	return buildAggregatedNode(node.params, nodes, nil)
}

// RemoveTail removes the last compressed node and recomputes the path
// commitment. It does not change Next.
// RemoveTail 只删除聚合序列最后一个节点；Next 保持不变。
func RemoveTail(node *AggregatedNode) (*AggregatedNode, MPTNodeLike, error) {
	if node == nil {
		return nil, nil, fmt.Errorf("nil aggregated node")
	}
	if node.NodeCount == 0 {
		return nil, nil, fmt.Errorf("empty aggregated node")
	}
	if len(node.Nodes) != node.NodeCount {
		return nil, nil, fmt.Errorf("node sequence metadata mismatch")
	}
	if node.NodeCount == 1 {
		return nil, nil, fmt.Errorf("cannot remove the only compressed node")
	}
	removed := node.Nodes[node.NodeCount-1]
	remaining := cloneNodes(node.Nodes[:node.NodeCount-1])
	next, err := buildAggregatedNode(node.params, remaining, node.Next)
	if err != nil {
		return nil, nil, err
	}
	return next, removed, nil
}

// CanonicalPathBytes encodes a node sequence without ambiguity by prefixing
// each node encoding with its uint64 big-endian length.
// CanonicalPathBytes 使用“长度前缀 + 节点编码”的方式拼接路径，避免不同节点序列编码歧义。
func CanonicalPathBytes(nodes []MPTNodeLike) ([]byte, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("empty MPT path")
	}
	var out bytes.Buffer
	for i := range nodes {
		encoded, err := nodes[i].Encode()
		if err != nil {
			return nil, fmt.Errorf("encode MPT node %d: %w", i, err)
		}
		var prefix [8]byte
		binary.BigEndian.PutUint64(prefix[:], uint64(len(encoded)))
		out.Write(prefix[:])
		out.Write(encoded)
	}
	return out.Bytes(), nil
}

// BytesToFrChunks converts data into 31-byte big-endian bn254/fr chunks using
// the existing project helper for this byte-to-field mapping.
// BytesToFrChunks 复用项目已有的 31 字节到 fr.Element 转换逻辑。
func BytesToFrChunks(data []byte) ([]fr.Element, error) {
	return fileipa.BytesToFrChunks(data)
}

func compressedPrefix(path []MPTNodeLike, stop StopFunc) ([]MPTNodeLike, MPTNodeLike, error) {
	if len(path) == 0 {
		return nil, nil, fmt.Errorf("empty MPT path")
	}
	for i, node := range path {
		if stop != nil && stop(node) {
			if i == 0 {
				return nil, node, fmt.Errorf("stop policy matched first node; no non-empty aggregated node can be generated")
			}
			return cloneNodes(path[:i]), node, nil
		}
	}
	return cloneNodes(path), nil, nil
}

func buildAggregatedNode(params *CommitmentParams, nodes []MPTNodeLike, next MPTNodeLike) (*AggregatedNode, error) {
	if params == nil {
		return nil, fmt.Errorf("nil commitment params")
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("empty compressed node sequence")
	}
	commitment, elements, err := CommitPath(params, nodes)
	if err != nil {
		return nil, err
	}
	canonical, err := CanonicalPathBytes(nodes)
	if err != nil {
		return nil, err
	}
	hashes := make([][]byte, len(nodes))
	for i := range nodes {
		hashes[i] = append([]byte(nil), nodes[i].Hash()...)
	}
	var nextHash []byte
	if next != nil {
		nextHash = append([]byte(nil), next.Hash()...)
	}
	return &AggregatedNode{
		Path:               pathBytes(nodes),
		NodeCount:          len(nodes),
		OriginalNodeHashes: hashes,
		Commitment:         commitment,
		ChunkCount:         chunkCount(len(canonical)),
		ElementCount:       len(elements),
		Next:               next,
		NextHash:           nextHash,
		Nodes:              cloneNodes(nodes),
		params:             params,
	}, nil
}

// pathBytes 拼接路径中每个节点的 PathFragment，作为聚合节点的路径标识。
func pathBytes(nodes []MPTNodeLike) []byte {
	var out bytes.Buffer
	for i := range nodes {
		out.Write(nodes[i].PathFragment())
	}
	return out.Bytes()
}

// chunkCount 计算 byteLen 需要多少个 31-byte chunks。
func chunkCount(byteLen int) int {
	if byteLen == 0 {
		return 0
	}
	return (byteLen + frChunkSize - 1) / frChunkSize
}

// padElementsToPowerOfTwo 将元素向量补零到 2 的幂，以满足当前 commitment 后端的向量长度要求。
func padElementsToPowerOfTwo(in []fr.Element) []fr.Element {
	if len(in) == 0 {
		return nil
	}
	n := 1
	for n < len(in) {
		n <<= 1
	}
	out := make([]fr.Element, n)
	copy(out, in)
	return out
}

func cloneNodes(nodes []MPTNodeLike) []MPTNodeLike {
	return append([]MPTNodeLike(nil), nodes...)
}
