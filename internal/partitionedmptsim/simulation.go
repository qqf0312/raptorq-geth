package partitionedmptsim

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/internal/ipa"
	"github.com/ethereum/go-ethereum/internal/mptagg"
	"github.com/ethereum/go-ethereum/internal/partitioning"
)

type SimulationConfig struct {
	PartitionCount int
	SortBy         string
	Optimization   partitioning.OptimizationConfig
	LocalView      mptagg.CompressOptions
}

type SimulationResult struct {
	Paths []partitioning.StatePath

	Partitions  []partitioning.Partition
	Plans       []partitioning.SuperNodeReallocationPlan
	HighRisk    []partitioning.NodeWeight
	SuperNodes  []partitioning.SuperNode
	RawNodeSets []map[string]bool

	LocalViews []*mptagg.LocalPartitionView
}

type MPTVersion struct {
	Height int64
	RootID string

	// Paths are the StatePaths belonging to this MPT root.
	Paths []partitioning.StatePath
}

type MPTForestState struct {
	Versions     []MPTVersion
	LatestHeight int64

	Partitions []partitioning.Partition

	Plans      []partitioning.SuperNodeReallocationPlan
	HighNodes  []partitioning.NodeWeight
	SuperNodes []partitioning.SuperNode

	LocalViews map[int]*mptagg.LocalPartitionView
}

type MockMPT struct {
	Root *MockMPTNode
}

type MockMPTNode struct {
	Hash []byte

	StateKey []byte
	LeafHash []byte
	Size     int
	Raw      any

	Children []*MockMPTNode
}

// RunMockMPTSimulation starts from a full mock MPT, extracts root-to-leaf
// StatePaths, then delegates the rest of the pipeline to the partitioning and
// mptagg modules.
func RunMockMPTSimulation(mpt *MockMPT, cfg SimulationConfig) (*SimulationResult, error) {
	paths, err := ExtractStatePathsFromMockMPT(mpt)
	if err != nil {
		return nil, err
	}
	return RunPartitionedMPTSimulation(paths, cfg)
}

// ExtractStatePathsFromMockMPT walks a complete mock MPT and emits one
// StatePath for each root-to-leaf state path. This simulator adapter is the
// only place that understands the mock tree shape.
func ExtractStatePathsFromMockMPT(mpt *MockMPT) ([]partitioning.StatePath, error) {
	if mpt == nil || mpt.Root == nil {
		return nil, fmt.Errorf("nil mock MPT root")
	}
	var paths []partitioning.StatePath
	if err := walkMockMPT(mpt.Root, nil, &paths); err != nil {
		return nil, err
	}
	return paths, nil
}

// RunPartitionedMPTSimulation wires partitioning and mptagg together for
// integration tests. It intentionally keeps both modules decoupled: the first
// partitioning and SuperNode placement are delegated to partitioning, while
// local compressed views are delegated to mptagg.
func RunPartitionedMPTSimulation(paths []partitioning.StatePath, cfg SimulationConfig) (*SimulationResult, error) {
	if cfg.PartitionCount <= 0 {
		return nil, fmt.Errorf("invalid PartitionCount %d", cfg.PartitionCount)
	}
	sortBy := cfg.SortBy
	if sortBy == "" {
		sortBy = partitioning.SortByKey
	}

	partitions, err := partitioning.BuildPartitions(paths, cfg.PartitionCount, sortBy)
	if err != nil {
		return nil, err
	}
	plans, high, superNodes, err := partitioning.OptimizeSuperNodeDistribution(paths, partitions, cfg.Optimization)
	if err != nil {
		return nil, err
	}
	latestReachable := partitioning.BuildLatestReachableNodeHashSet(paths)
	rawNodeSets := partitioning.BuildRawNodeSets(partitions, plans, latestReachable, preserveLatestEnabled(cfg.LocalView))
	views := make([]*mptagg.LocalPartitionView, len(partitions))
	for i := range partitions {
		view, err := mptagg.BuildLocalPartitionViewFromRawNodeSet(paths, rawNodeSets[i], partitions[i].ID, cfg.LocalView)
		if err != nil {
			return nil, fmt.Errorf("build local view for partition %d: %w", partitions[i].ID, err)
		}
		views[i] = view
	}
	return &SimulationResult{
		Paths:       cloneSimulationPaths(paths),
		Partitions:  cloneSimulationPartitions(partitions),
		Plans:       cloneSimulationPlans(plans),
		HighRisk:    append([]partitioning.NodeWeight(nil), high...),
		SuperNodes:  cloneSimulationSuperNodes(superNodes),
		RawNodeSets: cloneSimulationRawNodeSets(rawNodeSets),
		LocalViews:  views,
	}, nil
}

// AssignNewPathByKey deterministically maps a new StatePath to an existing
// partition using only that path's own key material. It deliberately ignores
// reused old node hashes: shared nodes may affect local compressed views, but
// they do not inherit another path's primary partition.
//
// sortBy="key" hashes path.Key. sortBy="hash" hashes path.LeafHash, falling
// back to path.Key if LeafHash is empty. The partition is the last 8 bytes of
// SHA-256 interpreted big-endian modulo partitionCount.
func AssignNewPathByKey(path partitioning.StatePath, partitionCount int, sortBy string) (int, error) {
	if partitionCount <= 0 {
		return 0, fmt.Errorf("invalid partitionCount %d", partitionCount)
	}
	if sortBy == "" {
		sortBy = partitioning.SortByKey
	}
	var key []byte
	switch sortBy {
	case partitioning.SortByKey:
		key = path.Key
	case partitioning.SortByHash:
		key = path.LeafHash
		if len(key) == 0 {
			key = path.Key
		}
	default:
		return 0, fmt.Errorf("unsupported sort mode %q", sortBy)
	}
	sum := sha256.Sum256(key)
	value := binary.BigEndian.Uint64(sum[len(sum)-8:])
	return int(value % uint64(partitionCount)), nil
}

func AddLatestMPTAndUpdateForest(oldState *MPTForestState, newVersion MPTVersion, partitionCount int, sortBy string, cfg partitioning.OptimizationConfig) (*MPTForestState, error) {
	return AddLatestMPTByKeyPartition(oldState, newVersion, partitionCount, sortBy, cfg)
}

func AddLatestMPTByKeyPartition(oldState *MPTForestState, newVersion MPTVersion, partitionCount int, sortBy string, cfg partitioning.OptimizationConfig) (*MPTForestState, error) {
	if partitionCount <= 0 {
		return nil, fmt.Errorf("invalid partitionCount %d", partitionCount)
	}
	if sortBy == "" {
		sortBy = partitioning.SortByKey
	}

	version := cloneMPTVersion(newVersion)
	var versions []MPTVersion
	var partitions []partitioning.Partition
	var plans []partitioning.SuperNodeReallocationPlan
	var high []partitioning.NodeWeight
	var superNodes []partitioning.SuperNode
	if oldState == nil || len(oldState.Partitions) == 0 {
		var err error
		partitions, err = partitioning.BuildPartitions(version.Paths, partitionCount, sortBy)
		if err != nil {
			return nil, err
		}
		if oldState != nil {
			versions = cloneMPTVersions(oldState.Versions)
		}
		versions = append(versions, version)
		plans, high, superNodes, err = partitioning.OptimizeSuperNodeDistribution(version.Paths, partitions, cfg)
		if err != nil {
			return nil, err
		}
	} else {
		if len(oldState.Partitions) != partitionCount {
			return nil, fmt.Errorf("partitionCount %d does not match existing partition count %d", partitionCount, len(oldState.Partitions))
		}
		versions = cloneMPTVersions(oldState.Versions)
		partitions = cloneSimulationPartitions(oldState.Partitions)
		for i := range version.Paths {
			selected, err := AssignNewPathByKey(version.Paths[i], partitionCount, sortBy)
			if err != nil {
				return nil, err
			}
			partitions[selected].Paths = append(partitions[selected].Paths, cloneSimulationPath(version.Paths[i]))
			refreshPartitionRange(&partitions[selected])
		}
		versions = append(versions, version)
		newPlans, newHigh, newSuperNodes, err := partitioning.OptimizeSuperNodeDistribution(version.Paths, partitions, cfg)
		if err != nil {
			return nil, err
		}
		plans = append(cloneSimulationPlans(oldState.Plans), newPlans...)
		high = append([]partitioning.NodeWeight(nil), oldState.HighNodes...)
		high = append(high, newHigh...)
		superNodes = append(cloneSimulationSuperNodes(oldState.SuperNodes), newSuperNodes...)
	}

	forestPaths := collectPartitionPaths(partitions)
	localOpts, err := forestLocalViewOptions(forestPaths)
	if err != nil {
		return nil, err
	}
	localOpts.LatestReachable = BuildLatestReachableSet(version)
	localOpts.RespectLatestBoundary = true
	localOpts.PathVersions = buildPathVersions(versions)
	localOpts.LatestHeight = version.Height
	localOpts.PreserveLatest = true
	localOpts.PreserveLatestSet = true
	views, err := buildForestLocalViews(forestPaths, partitions, plans, localOpts)
	if err != nil {
		return nil, err
	}

	return &MPTForestState{
		Versions:     versions,
		LatestHeight: version.Height,
		Partitions:   cloneSimulationPartitions(partitions),
		Plans:        cloneSimulationPlans(plans),
		HighNodes:    append([]partitioning.NodeWeight(nil), high...),
		SuperNodes:   cloneSimulationSuperNodes(superNodes),
		LocalViews:   views,
	}, nil
}

func BuildLatestReachableSet(latest MPTVersion) map[string]bool {
	out := make(map[string]bool)
	for i := range latest.Paths {
		for j := range latest.Paths[i].Nodes {
			if len(latest.Paths[i].Nodes[j].Hash) == 0 {
				continue
			}
			out[hex.EncodeToString(latest.Paths[i].Nodes[j].Hash)] = true
		}
	}
	return out
}

func walkMockMPT(node *MockMPTNode, stack []*MockMPTNode, out *[]partitioning.StatePath) error {
	if node == nil {
		return fmt.Errorf("nil mock MPT node")
	}
	stack = append(stack, node)
	if len(node.Children) == 0 {
		path, err := statePathFromMockStack(stack)
		if err != nil {
			return err
		}
		*out = append(*out, path)
		return nil
	}
	for i := range node.Children {
		if err := walkMockMPT(node.Children[i], stack, out); err != nil {
			return err
		}
	}
	return nil
}

func collectPartitionPaths(partitions []partitioning.Partition) []partitioning.StatePath {
	total := 0
	for i := range partitions {
		total += len(partitions[i].Paths)
	}
	out := make([]partitioning.StatePath, 0, total)
	for i := range partitions {
		out = append(out, cloneSimulationPaths(partitions[i].Paths)...)
	}
	return out
}

func refreshPartitionRange(partition *partitioning.Partition) {
	partition.StartIndex = 0
	partition.EndIndex = len(partition.Paths)
	if len(partition.Paths) == 0 {
		partition.StartKey = nil
		partition.EndKey = nil
		return
	}
	partition.StartKey = append(partition.StartKey[:0], partition.Paths[0].Key...)
	partition.EndKey = append(partition.EndKey[:0], partition.Paths[len(partition.Paths)-1].Key...)
}

func buildForestLocalViews(paths []partitioning.StatePath, partitions []partitioning.Partition, plans []partitioning.SuperNodeReallocationPlan, opts mptagg.CompressOptions) (map[int]*mptagg.LocalPartitionView, error) {
	views := make(map[int]*mptagg.LocalPartitionView, len(partitions))
	rawNodeSets := partitioning.BuildRawNodeSets(partitions, plans, latestReachableNodeHashSetForOptions(paths, opts), preserveLatestEnabled(opts))
	for i := range partitions {
		view, err := mptagg.BuildLocalPartitionViewFromRawNodeSet(paths, rawNodeSets[i], partitions[i].ID, opts)
		if err != nil {
			return nil, fmt.Errorf("build local view for partition %d: %w", partitions[i].ID, err)
		}
		views[partitions[i].ID] = view
	}
	return views, nil
}

func latestReachableNodeHashSetForOptions(paths []partitioning.StatePath, opts mptagg.CompressOptions) map[partitioning.NodeHash]bool {
	if len(opts.LatestReachable) == 0 {
		return partitioning.BuildLatestReachableNodeHashSet(paths)
	}
	out := make(map[partitioning.NodeHash]bool, len(opts.LatestReachable))
	for hash := range opts.LatestReachable {
		decoded, err := hex.DecodeString(hash)
		if err != nil {
			continue
		}
		out[common.BytesToHash(decoded)] = true
	}
	return out
}

func preserveLatestEnabled(opts mptagg.CompressOptions) bool {
	if opts.PreserveLatestSet {
		return opts.PreserveLatest
	}
	return true
}

func buildPathVersions(versions []MPTVersion) map[string]int64 {
	out := make(map[string]int64)
	for i := range versions {
		for j := range versions[i].Paths {
			out[mptagg.PathID(versions[i].Paths[j])] = versions[i].Height
		}
	}
	return out
}

func forestLocalViewOptions(paths []partitioning.StatePath) (mptagg.CompressOptions, error) {
	n := nextPowerOfTwo(maxPathElementCount(paths))
	params, err := ipa.NewTestParams(n)
	if err != nil {
		return mptagg.CompressOptions{}, err
	}
	return mptagg.CompressOptions{Params: params}, nil
}

func maxPathElementCount(paths []partitioning.StatePath) int {
	maxChunks := 1
	for i := range paths {
		byteLen := 0
		for j := range paths[i].Nodes {
			node := paths[i].Nodes[j]
			pathKey := node.PathKey
			if len(pathKey) == 0 {
				pathKey = paths[i].Key
			}
			encodedLen := 40 + len(pathKey) + len(node.Hash)
			byteLen += 8 + encodedLen
		}
		chunks := (byteLen + 31 - 1) / 31
		if chunks > maxChunks {
			maxChunks = chunks
		}
		withPrefixZeros := chunks + len(paths[i].Nodes)
		if withPrefixZeros > maxChunks {
			maxChunks = withPrefixZeros
		}
	}
	return maxChunks
}

func nextPowerOfTwo(n int) int {
	if n <= 1 {
		return 1
	}
	out := 1
	for out < n {
		out <<= 1
	}
	return out
}

func statePathFromMockStack(stack []*MockMPTNode) (partitioning.StatePath, error) {
	leaf := stack[len(stack)-1]
	if len(leaf.StateKey) == 0 {
		return partitioning.StatePath{}, fmt.Errorf("leaf %x missing StateKey", leaf.Hash)
	}
	nodes := make([]partitioning.PathNode, len(stack))
	nodeHashes := make([][]byte, len(stack))
	pathNibbles := make([]byte, len(stack))
	totalSize := 0
	for i := range stack {
		parent := i - 1
		if i == 0 {
			parent = -1
		}
		size := stack[i].Size
		if size == 0 {
			size = len(stack[i].Hash)
		}
		nodes[i] = partitioning.PathNode{
			Hash:           append([]byte(nil), stack[i].Hash...),
			PathKey:        append([]byte(nil), leaf.StateKey...),
			EdgeNibble:     0xff,
			Position:       i,
			ParentPosition: parent,
			ChildCount:     len(stack[i].Children),
			Size:           size,
			Raw:            stack[i].Raw,
		}
		nodeHashes[i] = append([]byte(nil), stack[i].Hash...)
		pathNibbles[i] = byte(i % 16)
		totalSize += size
	}
	leafHash := append([]byte(nil), leaf.LeafHash...)
	if len(leafHash) == 0 {
		leafHash = append([]byte(nil), leaf.Hash...)
	}
	return partitioning.StatePath{
		Key:         append([]byte(nil), leaf.StateKey...),
		PathNibbles: pathNibbles,
		NodeHashes:  nodeHashes,
		Nodes:       nodes,
		LeafHash:    leafHash,
		Size:        totalSize,
		Raw:         leaf.Raw,
	}, nil
}

func cloneSimulationPaths(paths []partitioning.StatePath) []partitioning.StatePath {
	out := make([]partitioning.StatePath, len(paths))
	for i := range paths {
		out[i] = cloneSimulationPath(paths[i])
	}
	return out
}

func cloneMPTVersions(versions []MPTVersion) []MPTVersion {
	out := make([]MPTVersion, len(versions))
	for i := range versions {
		out[i] = cloneMPTVersion(versions[i])
	}
	return out
}

func cloneMPTVersion(version MPTVersion) MPTVersion {
	return MPTVersion{
		Height: version.Height,
		RootID: version.RootID,
		Paths:  cloneSimulationPaths(version.Paths),
	}
}

func cloneSimulationPartitions(partitions []partitioning.Partition) []partitioning.Partition {
	out := make([]partitioning.Partition, len(partitions))
	for i := range partitions {
		out[i] = partitioning.Partition{
			ID:         partitions[i].ID,
			Paths:      cloneSimulationPaths(partitions[i].Paths),
			StartKey:   append([]byte(nil), partitions[i].StartKey...),
			EndKey:     append([]byte(nil), partitions[i].EndKey...),
			StartIndex: partitions[i].StartIndex,
			EndIndex:   partitions[i].EndIndex,
		}
	}
	return out
}

func cloneSimulationPlans(plans []partitioning.SuperNodeReallocationPlan) []partitioning.SuperNodeReallocationPlan {
	out := make([]partitioning.SuperNodeReallocationPlan, len(plans))
	for i := range plans {
		out[i] = partitioning.SuperNodeReallocationPlan{
			SuperNode:          cloneSimulationSuperNode(plans[i].SuperNode),
			PreferredPartition: plans[i].PreferredPartition,
			AssignedPartition:  plans[i].AssignedPartition,
			Reason:             plans[i].Reason,
		}
	}
	return out
}

func cloneSimulationSuperNodes(superNodes []partitioning.SuperNode) []partitioning.SuperNode {
	out := make([]partitioning.SuperNode, len(superNodes))
	for i := range superNodes {
		out[i] = cloneSimulationSuperNode(superNodes[i])
	}
	return out
}

func cloneSimulationRawNodeSets(sets []map[string]bool) []map[string]bool {
	out := make([]map[string]bool, len(sets))
	for i := range sets {
		out[i] = make(map[string]bool, len(sets[i]))
		for hash, keepRaw := range sets[i] {
			out[i][hash] = keepRaw
		}
	}
	return out
}

func cloneSimulationSuperNode(superNode partitioning.SuperNode) partitioning.SuperNode {
	return partitioning.SuperNode{
		PathKey:         append([]byte(nil), superNode.PathKey...),
		StartPosition:   superNode.StartPosition,
		EndPosition:     superNode.EndPosition,
		Nodes:           cloneSimulationNodes(superNode.Nodes),
		ParentHash:      append([]byte(nil), superNode.ParentHash...),
		ParentPosition:  superNode.ParentPosition,
		ParentPartition: superNode.ParentPartition,
		OriginalHolder:  superNode.OriginalHolder,
		Weight:          superNode.Weight,
		TotalSize:       superNode.TotalSize,
	}
}

func cloneSimulationPath(path partitioning.StatePath) partitioning.StatePath {
	nodeHashes := make([][]byte, len(path.NodeHashes))
	for i := range path.NodeHashes {
		nodeHashes[i] = append([]byte(nil), path.NodeHashes[i]...)
	}
	return partitioning.StatePath{
		Key:         append([]byte(nil), path.Key...),
		PathNibbles: append([]byte(nil), path.PathNibbles...),
		NodeHashes:  nodeHashes,
		Nodes:       cloneSimulationNodes(path.Nodes),
		LeafHash:    append([]byte(nil), path.LeafHash...),
		Size:        path.Size,
		Raw:         path.Raw,
	}
}

func cloneSimulationNodes(nodes []partitioning.PathNode) []partitioning.PathNode {
	out := make([]partitioning.PathNode, len(nodes))
	for i := range nodes {
		out[i] = partitioning.PathNode{
			Hash:           append([]byte(nil), nodes[i].Hash...),
			PathKey:        append([]byte(nil), nodes[i].PathKey...),
			NodeType:       nodes[i].NodeType,
			EdgeNibble:     nodes[i].EdgeNibble,
			PrefixNibbles:  append([]byte(nil), nodes[i].PrefixNibbles...),
			Position:       nodes[i].Position,
			ParentPosition: nodes[i].ParentPosition,
			ChildCount:     nodes[i].ChildCount,
			Size:           nodes[i].Size,
			Raw:            nodes[i].Raw,
		}
	}
	return out
}
