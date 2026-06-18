package partitioning

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

type NodeHash = common.Hash

// MPTStatePathRoot is the root handle surface needed by MPTPartitionManager.
// Implementations are expected to use the project's trie/MPT traversal to
// produce StatePaths from the root.
type MPTStatePathRoot interface {
	RootHashString() string
	ExtractStatePaths() ([]StatePath, error)
}

// MPTUpdatedNodeSetRoot is an optional extension for roots backed by a native
// go-ethereum trie commit. When available, the partition manager uses the
// commit NodeSet as the source of updated raw MPT node hashes and verifies it
// matches the StatePath-derived result.
type MPTUpdatedNodeSetRoot interface {
	UpdatedNodeSet() *trienode.NodeSet
}

type MPTPartitionManager struct {
	PartitionCount int
	SortBy         string
	Config         OptimizationConfig

	PreserveLatest    bool
	PreserveLatestSet bool

	CurrentRootHash string
	CurrentPaths    []StatePath

	Partitions  []Partition
	RawNodeSets []map[string]bool

	Plans      []SuperNodeReallocationPlan
	HighNodes  []NodeWeight
	SuperNodes []SuperNode
}

type StatePathDiff struct {
	Changed   []StatePath
	Unchanged []StatePath
	Removed   []StatePath

	AddedNodeHashes   map[NodeHash]bool
	ReusedNodeHashes  map[NodeHash]bool
	RemovedNodeHashes map[NodeHash]bool
}

type MPTPartitionResult struct {
	RootHash         string
	PreviousRootHash string

	LatestPaths   []StatePath
	PreviousPaths []StatePath
	Diff          StatePathDiff

	Partitions []Partition
	Plans      []SuperNodeReallocationPlan
	HighNodes  []NodeWeight
	SuperNodes []SuperNode

	LatestReachable map[NodeHash]bool
	RawNodeSets     []map[string]bool
}

func NewMPTPartitionManager(partitionCount int, sortBy string, cfg OptimizationConfig) (*MPTPartitionManager, error) {
	if partitionCount <= 0 {
		return nil, fmt.Errorf("invalid partitionCount %d", partitionCount)
	}
	if sortBy == "" {
		sortBy = SortByKey
	}
	if err := validateSortBy(sortBy); err != nil {
		return nil, err
	}
	return &MPTPartitionManager{
		PartitionCount: partitionCount,
		SortBy:         sortBy,
		Config:         cfg,
	}, nil
}

func (m *MPTPartitionManager) AddLatestRoot(latestRoot MPTStatePathRoot) (*MPTPartitionResult, error) {
	if latestRoot == nil {
		return nil, fmt.Errorf("nil latest root")
	}
	if m.PartitionCount <= 0 {
		return nil, fmt.Errorf("invalid PartitionCount %d", m.PartitionCount)
	}
	sortBy := m.SortBy
	if sortBy == "" {
		sortBy = SortByKey
	}
	if err := validateSortBy(sortBy); err != nil {
		return nil, err
	}

	latestPaths, err := latestRoot.ExtractStatePaths()
	if err != nil {
		return nil, err
	}
	previousRootHash := m.CurrentRootHash
	previousPaths := cloneStatePaths(m.CurrentPaths)
	diff := DiffStatePaths(previousPaths, latestPaths)
	if err := applyNativeUpdatedNodeSet(latestRoot, &diff); err != nil {
		return nil, err
	}

	partitions, err := BuildPartitions(diff.Changed, m.PartitionCount, sortBy)
	if err != nil {
		return nil, err
	}
	plans, high, superNodes, err := OptimizeSuperNodeDistribution(diff.Changed, partitions, m.Config)
	if err != nil {
		return nil, err
	}
	latestReachable := BuildLatestReachableNodeHashSet(latestPaths)
	rawNodeSets := BuildRawNodeSets(partitions, plans, latestReachable, preserveLatestEnabled(m))

	m.CurrentRootHash = latestRoot.RootHashString()
	m.CurrentPaths = cloneStatePaths(latestPaths)
	m.Partitions = clonePartitions(partitions)
	m.Plans = cloneReallocationPlans(plans)
	m.HighNodes = cloneNodeWeights(high)
	m.SuperNodes = cloneSuperNodes(superNodes)
	m.RawNodeSets = cloneRawNodeSets(rawNodeSets)

	return &MPTPartitionResult{
		RootHash:         m.CurrentRootHash,
		PreviousRootHash: previousRootHash,
		LatestPaths:      cloneStatePaths(latestPaths),
		PreviousPaths:    previousPaths,
		Diff:             cloneStatePathDiff(diff),
		Partitions:       clonePartitions(partitions),
		Plans:            cloneReallocationPlans(plans),
		HighNodes:        cloneNodeWeights(high),
		SuperNodes:       cloneSuperNodes(superNodes),
		LatestReachable:  cloneNodeHashSet(latestReachable),
		RawNodeSets:      cloneRawNodeSets(rawNodeSets),
	}, nil
}

func DiffStatePaths(oldPaths []StatePath, newPaths []StatePath) StatePathDiff {
	oldByKey := make(map[string]StatePath, len(oldPaths))
	oldNodeHashes := make(map[NodeHash]bool)
	for i := range oldPaths {
		oldByKey[string(oldPaths[i].Key)] = oldPaths[i]
		for _, hash := range pathNodeHashes(oldPaths[i]) {
			oldNodeHashes[hash] = true
		}
	}

	newByKey := make(map[string]StatePath, len(newPaths))
	newNodeHashes := make(map[NodeHash]bool)
	out := StatePathDiff{
		AddedNodeHashes:   make(map[NodeHash]bool),
		ReusedNodeHashes:  make(map[NodeHash]bool),
		RemovedNodeHashes: make(map[NodeHash]bool),
	}
	for i := range newPaths {
		path := newPaths[i]
		newByKey[string(path.Key)] = path
		old, ok := oldByKey[string(path.Key)]
		if !ok {
			out.Changed = append(out.Changed, cloneStatePath(path))
		} else if sameTerminalLeafHash(old, path) {
			out.Unchanged = append(out.Unchanged, cloneStatePath(path))
		} else {
			out.Changed = append(out.Changed, cloneStatePath(path))
		}
		for _, hash := range pathNodeHashes(path) {
			newNodeHashes[hash] = true
			if oldNodeHashes[hash] {
				out.ReusedNodeHashes[hash] = true
			} else {
				out.AddedNodeHashes[hash] = true
			}
		}
	}
	for i := range oldPaths {
		path := oldPaths[i]
		if _, ok := newByKey[string(path.Key)]; !ok {
			out.Removed = append(out.Removed, cloneStatePath(path))
		}
		for _, hash := range pathNodeHashes(path) {
			if !newNodeHashes[hash] {
				out.RemovedNodeHashes[hash] = true
			}
		}
	}
	sortDiffPaths(&out)
	return out
}

func BuildLatestReachableNodeHashSet(paths []StatePath) map[NodeHash]bool {
	out := make(map[NodeHash]bool)
	for i := range paths {
		addPathNodeHashes(out, paths[i])
	}
	return out
}

func UpdatedNodeHashesFromNodeSet(nodes *trienode.NodeSet) map[NodeHash]bool {
	if nodes == nil {
		return nil
	}
	out := make(map[NodeHash]bool)
	for _, node := range nodes.Nodes {
		if node == nil || node.IsDeleted() || node.Hash == (common.Hash{}) {
			continue
		}
		out[node.Hash] = true
	}
	return out
}

func applyNativeUpdatedNodeSet(root MPTStatePathRoot, diff *StatePathDiff) error {
	provider, ok := root.(MPTUpdatedNodeSetRoot)
	if !ok {
		return nil
	}
	native := UpdatedNodeHashesFromNodeSet(provider.UpdatedNodeSet())
	if native == nil {
		return nil
	}
	if !nodeHashSetsEqual(native, diff.AddedNodeHashes) {
		return fmt.Errorf("native NodeSet updated hashes differ from StatePath diff: native=%d statepath=%d", len(native), len(diff.AddedNodeHashes))
	}
	diff.AddedNodeHashes = native
	return nil
}

func BuildRawNodeSets(partitions []Partition, plans []SuperNodeReallocationPlan, latestReachable map[NodeHash]bool, preserveLatest bool) []map[string]bool {
	nodeSets := make([]map[NodeHash]bool, len(partitions))
	idToIndex := make(map[int]int, len(partitions))
	for i := range partitions {
		nodeSets[i] = make(map[NodeHash]bool)
		idToIndex[partitions[i].ID] = i
		for j := range partitions[i].Paths {
			addPathNodeHashes(nodeSets[i], partitions[i].Paths[j])
		}
	}
	for i := range plans {
		idx, ok := idToIndex[plans[i].AssignedPartition]
		if !ok {
			continue
		}
		addNodeHashes(nodeSets[idx], plans[i].SuperNode.Nodes)
	}
	if preserveLatest {
		for i := range nodeSets {
			for hash := range latestReachable {
				nodeSets[i][hash] = true
			}
		}
	}
	out := make([]map[string]bool, len(nodeSets))
	for i := range nodeSets {
		out[i] = nodeHashSetToStringSet(nodeSets[i])
	}
	return out
}

func nodeHashSetToStringSet(in map[NodeHash]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for hash, keep := range in {
		if keep {
			out[hash.Hex()] = true
			trimmed := bytes.TrimLeft(hash.Bytes(), "\x00")
			if len(trimmed) > 0 {
				out[hexString(trimmed)] = true
			}
		}
	}
	return out
}

func hexString(b []byte) string {
	const hextable = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hextable[v>>4]
		out[i*2+1] = hextable[v&0x0f]
	}
	return string(out)
}

func preserveLatestEnabled(m *MPTPartitionManager) bool {
	if m.PreserveLatestSet {
		return m.PreserveLatest
	}
	return true
}

func sameTerminalLeafHash(a StatePath, b StatePath) bool {
	left, okLeft := terminalLeafHash(a)
	right, okRight := terminalLeafHash(b)
	return okLeft && okRight && left == right
}

func terminalLeafHash(path StatePath) (NodeHash, bool) {
	if len(path.LeafHash) > 0 {
		return common.BytesToHash(path.LeafHash), true
	}
	if len(path.Nodes) > 0 && len(path.Nodes[len(path.Nodes)-1].Hash) > 0 {
		return common.BytesToHash(path.Nodes[len(path.Nodes)-1].Hash), true
	}
	return NodeHash{}, false
}

func pathNodeHashes(path StatePath) []NodeHash {
	hashes := pathNodeHashBytes(path)
	out := make([]NodeHash, 0, len(hashes))
	for i := range hashes {
		if len(hashes[i]) == 0 {
			continue
		}
		out = append(out, common.BytesToHash(hashes[i]))
	}
	return out
}

func pathNodeHashBytes(path StatePath) [][]byte {
	if len(path.Nodes) > 0 {
		out := make([][]byte, 0, len(path.Nodes))
		for i := range path.Nodes {
			if len(path.Nodes[i].Hash) == 0 {
				continue
			}
			out = append(out, path.Nodes[i].Hash)
		}
		return out
	}
	return path.NodeHashes
}

func addPathNodeHashes(dst map[NodeHash]bool, path StatePath) {
	for _, hash := range pathNodeHashes(path) {
		dst[hash] = true
	}
}

func addNodeHashes(dst map[NodeHash]bool, nodes []PathNode) {
	for i := range nodes {
		if len(nodes[i].Hash) == 0 {
			continue
		}
		dst[common.BytesToHash(nodes[i].Hash)] = true
	}
}

func sortDiffPaths(diff *StatePathDiff) {
	sortStatePathSlice(diff.Changed)
	sortStatePathSlice(diff.Unchanged)
	sortStatePathSlice(diff.Removed)
}

func sortStatePathSlice(paths []StatePath) {
	sort.SliceStable(paths, func(i, j int) bool {
		return bytes.Compare(paths[i].Key, paths[j].Key) < 0
	})
}

func cloneStatePathDiff(diff StatePathDiff) StatePathDiff {
	return StatePathDiff{
		Changed:           cloneStatePaths(diff.Changed),
		Unchanged:         cloneStatePaths(diff.Unchanged),
		Removed:           cloneStatePaths(diff.Removed),
		AddedNodeHashes:   cloneNodeHashSet(diff.AddedNodeHashes),
		ReusedNodeHashes:  cloneNodeHashSet(diff.ReusedNodeHashes),
		RemovedNodeHashes: cloneNodeHashSet(diff.RemovedNodeHashes),
	}
}

func clonePartitions(partitions []Partition) []Partition {
	out := make([]Partition, len(partitions))
	for i := range partitions {
		out[i] = Partition{
			ID:         partitions[i].ID,
			Paths:      cloneStatePaths(partitions[i].Paths),
			StartKey:   append([]byte(nil), partitions[i].StartKey...),
			EndKey:     append([]byte(nil), partitions[i].EndKey...),
			StartIndex: partitions[i].StartIndex,
			EndIndex:   partitions[i].EndIndex,
		}
	}
	return out
}

func cloneReallocationPlans(plans []SuperNodeReallocationPlan) []SuperNodeReallocationPlan {
	out := make([]SuperNodeReallocationPlan, len(plans))
	for i := range plans {
		out[i] = SuperNodeReallocationPlan{
			SuperNode:          cloneSuperNode(plans[i].SuperNode),
			PreferredPartition: plans[i].PreferredPartition,
			AssignedPartition:  plans[i].AssignedPartition,
			Reason:             plans[i].Reason,
		}
	}
	return out
}

func cloneNodeWeights(weights []NodeWeight) []NodeWeight {
	out := make([]NodeWeight, len(weights))
	for i := range weights {
		out[i] = cloneNodeWeight(weights[i])
	}
	return out
}

func cloneRawNodeSets(sets []map[string]bool) []map[string]bool {
	out := make([]map[string]bool, len(sets))
	for i := range sets {
		out[i] = cloneStringBoolMap(sets[i])
	}
	return out
}

func cloneStringBoolMap(values map[string]bool) map[string]bool {
	if values == nil {
		return nil
	}
	out := make(map[string]bool, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneNodeHashSet(values map[NodeHash]bool) map[NodeHash]bool {
	if values == nil {
		return nil
	}
	out := make(map[NodeHash]bool, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func nodeHashSetsEqual(a, b map[NodeHash]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for hash, keep := range a {
		if keep != b[hash] {
			return false
		}
	}
	return true
}
