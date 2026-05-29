package partitioning

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
)

type PathReplicaInfo struct {
	PathKey      []byte
	PathHash     []byte
	ReplicaCount int
	Holders      []string
}

type StorageNode struct {
	ID            string
	FailureDomain string
	Capacity      int64
	Used          int64
	Load          float64
}

type PathWeight struct {
	Path         StatePath
	ReplicaCount int
	Weight       float64
	Holders      []string
}

type OptimizationConfig struct {
	Beta               float64
	HighWeightRatio    float64
	TargetReplicaCount int

	HighRiskMultiplier float64
	MaxSuperNodeLength int

	CandidateStorageNodes []StorageNode
}

// NodeReplicaInfo records which first-stage partition holders contain a
// PathNode. Node risk weight is based only on replicaCount from the first
// partition result.
type NodeReplicaInfo struct {
	NodeHash []byte
	PathKey  []byte
	Position int

	ReplicaCount int
	Holders      []string
}

type NodeWeight struct {
	PathKey []byte
	Node    PathNode

	ReplicaCount int
	Weight       float64

	Holders []string
}

// SuperNode is the second-stage reallocation unit. It is a continuous segment
// on one StatePath, saved in root -> leaf order.
type SuperNode struct {
	PathKey []byte

	StartPosition int
	EndPosition   int

	Nodes []PathNode

	ParentHash      []byte
	ParentPosition  int
	ParentPartition int
	OriginalHolder  string

	Weight    float64
	TotalSize int
}

type SuperNodeConfig struct {
	MaxSuperNodeLength int
}

type SuperNodeReallocationPlan struct {
	SuperNode SuperNode

	PreferredPartition int
	AssignedPartition  int

	Reason string
}

type PathReallocationPlan struct {
	Path StatePath

	CurrentReplicaCount    int
	TargetReplicaCount     int
	AdditionalReplicaCount int

	ExistingHolders []string
	NewHolders      []string
}

func ComputePathWeight(replicaCount int, beta float64) (float64, error) {
	if replicaCount < 0 {
		return 0, fmt.Errorf("negative replicaCount %d", replicaCount)
	}
	if beta <= 0 {
		return 0, fmt.Errorf("invalid beta %f", beta)
	}
	return 1 / math.Pow(1+float64(replicaCount), beta), nil
}

func ComputeNodeWeight(replicaCount int, beta float64) (float64, error) {
	if replicaCount < 0 {
		return 0, fmt.Errorf("negative replicaCount %d", replicaCount)
	}
	if beta <= 0 {
		return 0, fmt.Errorf("invalid beta %f", beta)
	}
	return 1 / math.Pow(1+float64(replicaCount), beta), nil
}

// BuildNodeReplicaMapFromPartitions derives PathNode holder counts from the
// first StatePath-granular partitioning result. It does not repartition paths.
func BuildNodeReplicaMapFromPartitions(partitions []Partition) map[string]NodeReplicaInfo {
	type mutableInfo struct {
		info    NodeReplicaInfo
		holders map[string]struct{}
	}
	tmp := make(map[string]*mutableInfo)
	for i := range partitions {
		holder := fmt.Sprintf("partition-%d", partitions[i].ID)
		for j := range partitions[i].Paths {
			path := partitions[i].Paths[j]
			for k := range path.Nodes {
				node := path.Nodes[k]
				pathKey := effectivePathKey(path, node)
				key := nodeReplicaKey(pathKey, node)
				entry, ok := tmp[key]
				if !ok {
					entry = &mutableInfo{
						info: NodeReplicaInfo{
							NodeHash: append([]byte(nil), node.Hash...),
							PathKey:  append([]byte(nil), pathKey...),
							Position: node.Position,
						},
						holders: make(map[string]struct{}),
					}
					tmp[key] = entry
				}
				entry.holders[holder] = struct{}{}
			}
		}
	}

	out := make(map[string]NodeReplicaInfo, len(tmp))
	for key, entry := range tmp {
		holders := make([]string, 0, len(entry.holders))
		for holder := range entry.holders {
			holders = append(holders, holder)
		}
		sort.Strings(holders)
		info := entry.info
		info.Holders = holders
		info.ReplicaCount = len(holders)
		out[key] = info
	}
	return out
}

func ComputeNodeWeights(paths []StatePath, replicaMap map[string]NodeReplicaInfo, beta float64) ([]NodeWeight, error) {
	if beta <= 0 {
		return nil, fmt.Errorf("invalid beta %f", beta)
	}
	out := make([]NodeWeight, 0)
	for i := range paths {
		path := paths[i]
		for j := range path.Nodes {
			node := path.Nodes[j]
			pathKey := effectivePathKey(path, node)
			info, ok := replicaMap[nodeReplicaKey(pathKey, node)]
			replicaCount := 0
			var holders []string
			if ok {
				if info.ReplicaCount < 0 {
					return nil, fmt.Errorf("negative replicaCount %d for node %x", info.ReplicaCount, node.Hash)
				}
				replicaCount = info.ReplicaCount
				holders = append([]string(nil), info.Holders...)
			}
			weight, err := ComputeNodeWeight(replicaCount, beta)
			if err != nil {
				return nil, err
			}
			copied := clonePathNode(node)
			copied.PathKey = append([]byte(nil), pathKey...)
			out = append(out, NodeWeight{
				PathKey:      append([]byte(nil), pathKey...),
				Node:         copied,
				ReplicaCount: replicaCount,
				Weight:       weight,
				Holders:      holders,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return compareNodeWeights(out[i], out[j], false) < 0
	})
	return out, nil
}

func SelectHighRiskNodes(weights []NodeWeight, multiplier float64) ([]NodeWeight, error) {
	if multiplier <= 0 {
		return nil, fmt.Errorf("invalid multiplier %f", multiplier)
	}
	if len(weights) == 0 {
		return []NodeWeight{}, nil
	}
	var sum float64
	for i := range weights {
		sum += weights[i].Weight
	}
	threshold := (sum / float64(len(weights))) * multiplier
	out := make([]NodeWeight, 0)
	for i := range weights {
		if weights[i].Weight >= threshold {
			out = append(out, cloneNodeWeight(weights[i]))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return compareNodeWeights(out[i], out[j], true) < 0
	})
	return out, nil
}

// BuildSuperNodesFromLeaf constructs SuperNodes from leaf-side high-risk nodes
// and merges upward through single-child parents. Reallocation is
// SuperNode-granular and placement later prefers the parent partition.
func BuildSuperNodesFromLeaf(paths []StatePath, high []NodeWeight, partitions []Partition, maxLen int) ([]SuperNode, error) {
	if maxLen <= 0 {
		return nil, fmt.Errorf("invalid maxLen %d", maxLen)
	}
	pathByKey := make(map[string]StatePath, len(paths))
	for i := range paths {
		pathByKey[string(paths[i].Key)] = cloneStatePath(paths[i])
	}
	highByPath := make(map[string][]NodeWeight)
	weightByNode := make(map[string]float64, len(high))
	for i := range high {
		pathKey := high[i].PathKey
		if len(pathKey) == 0 {
			pathKey = high[i].Node.PathKey
		}
		highByPath[string(pathKey)] = append(highByPath[string(pathKey)], cloneNodeWeight(high[i]))
		weightByNode[nodePositionKey(pathKey, high[i].Node)] = high[i].Weight
	}

	superNodes := make([]SuperNode, 0)
	for key, pathHigh := range highByPath {
		path, ok := pathByKey[key]
		if !ok {
			continue
		}
		sort.SliceStable(pathHigh, func(i, j int) bool {
			if pathHigh[i].Node.Position != pathHigh[j].Node.Position {
				return pathHigh[i].Node.Position > pathHigh[j].Node.Position
			}
			return compareNodeWeights(pathHigh[i], pathHigh[j], false) < 0
		})
		covered := make(map[int]struct{})
		for i := range pathHigh {
			seedPos := pathHigh[i].Node.Position
			if _, ok := covered[seedPos]; ok {
				continue
			}
			posToIndex := positionsForPath(path)
			currentPos := seedPos
			segmentPositions := []int{seedPos}
			covered[seedPos] = struct{}{}
			for len(segmentPositions) < maxLen {
				idx, ok := posToIndex[currentPos]
				if !ok {
					break
				}
				parentPos := path.Nodes[idx].ParentPosition
				if parentPos < 0 {
					break
				}
				parentIdx, ok := posToIndex[parentPos]
				if !ok {
					break
				}
				parent := path.Nodes[parentIdx]
				if parent.ChildCount > 1 {
					break
				}
				if _, ok := covered[parent.Position]; ok {
					break
				}
				// TODO: enforce latest-root boundary when that concept is modeled here.
				segmentPositions = append([]int{parent.Position}, segmentPositions...)
				covered[parent.Position] = struct{}{}
				currentPos = parent.Position
			}
			superNodes = append(superNodes, buildSuperNode(path, segmentPositions, partitions, weightByNode))
		}
	}
	sort.SliceStable(superNodes, func(i, j int) bool {
		if cmp := bytes.Compare(superNodes[i].PathKey, superNodes[j].PathKey); cmp != 0 {
			return cmp < 0
		}
		if superNodes[i].StartPosition != superNodes[j].StartPosition {
			return superNodes[i].StartPosition < superNodes[j].StartPosition
		}
		return superNodes[i].EndPosition < superNodes[j].EndPosition
	})
	return superNodes, nil
}

func BuildSuperNodeReallocationPlans(superNodes []SuperNode, partitions []Partition, cfg OptimizationConfig) ([]SuperNodeReallocationPlan, error) {
	if len(partitions) == 0 {
		return nil, fmt.Errorf("empty partitions")
	}
	ordered := cloneSuperNodes(superNodes)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].StartPosition != ordered[j].StartPosition {
			return ordered[i].StartPosition < ordered[j].StartPosition
		}
		if cmp := bytes.Compare(ordered[i].PathKey, ordered[j].PathKey); cmp != 0 {
			return cmp < 0
		}
		return ordered[i].EndPosition < ordered[j].EndPosition
	})

	plans := make([]SuperNodeReallocationPlan, 0, len(ordered))
	for i := range ordered {
		preferred := ordered[i].ParentPartition
		assignedPartition, ok := selectLowestLoadPartitionExcluding(partitions, preferred)
		if !ok {
			continue
		}
		plans = append(plans, SuperNodeReallocationPlan{
			SuperNode:          cloneSuperNode(ordered[i]),
			PreferredPartition: preferred,
			AssignedPartition:  assignedPartition,
			Reason:             "assigned to lowest-load non-primary partition",
		})
	}
	return plans, nil
}

func OptimizeSuperNodeDistribution(paths []StatePath, partitions []Partition, cfg OptimizationConfig) ([]SuperNodeReallocationPlan, []NodeWeight, []SuperNode, error) {
	replicaMap := BuildNodeReplicaMapFromPartitions(partitions)
	weights, err := ComputeNodeWeights(paths, replicaMap, cfg.Beta)
	if err != nil {
		return nil, nil, nil, err
	}
	high, err := SelectHighRiskNodes(weights, cfg.HighRiskMultiplier)
	if err != nil {
		return nil, nil, nil, err
	}
	superNodes, err := BuildSuperNodesFromLeaf(paths, high, partitions, cfg.MaxSuperNodeLength)
	if err != nil {
		return nil, nil, nil, err
	}
	plans, err := BuildSuperNodeReallocationPlans(superNodes, partitions, cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	return plans, high, superNodes, nil
}

func ComputePathWeights(paths []StatePath, replicaMap map[string]PathReplicaInfo, beta float64) ([]PathWeight, error) {
	if beta <= 0 {
		return nil, fmt.Errorf("invalid beta %f", beta)
	}
	out := make([]PathWeight, len(paths))
	for i := range paths {
		info, ok := replicaMap[replicaMapKey(paths[i])]
		replicaCount := 0
		var holders []string
		if ok {
			if info.ReplicaCount < 0 {
				return nil, fmt.Errorf("negative replicaCount %d for path %x", info.ReplicaCount, paths[i].Key)
			}
			replicaCount = info.ReplicaCount
			holders = append([]string(nil), info.Holders...)
		}
		weight, err := ComputePathWeight(replicaCount, beta)
		if err != nil {
			return nil, err
		}
		out[i] = PathWeight{
			Path:         cloneStatePath(paths[i]),
			ReplicaCount: replicaCount,
			Weight:       weight,
			Holders:      holders,
		}
	}
	return out, nil
}

func SelectHighWeightPaths(weights []PathWeight, ratio float64) ([]PathWeight, error) {
	if ratio <= 0 || ratio > 1 {
		return nil, fmt.Errorf("ratio must be in (0,1], got %f", ratio)
	}
	if len(weights) == 0 {
		return []PathWeight{}, nil
	}
	sorted := clonePathWeights(weights)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Weight != sorted[j].Weight {
			return sorted[i].Weight > sorted[j].Weight
		}
		return comparePathWeightTie(sorted[i], sorted[j]) < 0
	})
	count := int(math.Ceil(ratio * float64(len(sorted))))
	return clonePathWeights(sorted[:count]), nil
}

func BuildPathReallocationPlans(high []PathWeight, cfg OptimizationConfig) ([]PathReallocationPlan, error) {
	if cfg.TargetReplicaCount < 0 {
		return nil, fmt.Errorf("negative TargetReplicaCount %d", cfg.TargetReplicaCount)
	}
	plans := make([]PathReallocationPlan, 0)
	for i := range high {
		delta := cfg.TargetReplicaCount - high[i].ReplicaCount
		if delta <= 0 {
			continue
		}
		newHolders := selectNewHolders(high[i].Holders, cfg.CandidateStorageNodes, delta)
		if len(newHolders) == 0 {
			continue
		}
		plans = append(plans, PathReallocationPlan{
			Path:                   cloneStatePath(high[i].Path),
			CurrentReplicaCount:    high[i].ReplicaCount,
			TargetReplicaCount:     cfg.TargetReplicaCount,
			AdditionalReplicaCount: len(newHolders),
			ExistingHolders:        append([]string(nil), high[i].Holders...),
			NewHolders:             newHolders,
		})
	}
	return plans, nil
}

func OptimizePathDistribution(paths []StatePath, replicaMap map[string]PathReplicaInfo, cfg OptimizationConfig) ([]PathReallocationPlan, []PathWeight, error) {
	weights, err := ComputePathWeights(paths, replicaMap, cfg.Beta)
	if err != nil {
		return nil, nil, err
	}
	high, err := SelectHighWeightPaths(weights, cfg.HighWeightRatio)
	if err != nil {
		return nil, nil, err
	}
	plans, err := BuildPathReallocationPlans(high, cfg)
	if err != nil {
		return nil, nil, err
	}
	return plans, high, nil
}

func selectNewHolders(existing []string, candidates []StorageNode, count int) []string {
	existingSet := make(map[string]struct{}, len(existing))
	for _, holder := range existing {
		existingSet[holder] = struct{}{}
	}
	byID := make(map[string]StorageNode, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.ID] = candidate
	}
	selected := make([]string, 0, count)
	selectedDomains := make(map[string]struct{})
	for _, holder := range existing {
		if node, ok := byID[holder]; ok && node.FailureDomain != "" {
			selectedDomains[node.FailureDomain] = struct{}{}
		}
	}

	for len(selected) < count {
		available := availableStorageNodes(candidates, existingSet)
		if len(available) == 0 {
			break
		}
		sort.SliceStable(available, func(i, j int) bool {
			iSame := domainAlreadyUsed(available[i].FailureDomain, selectedDomains)
			jSame := domainAlreadyUsed(available[j].FailureDomain, selectedDomains)
			if iSame != jSame {
				return !iSame
			}
			if available[i].Load != available[j].Load {
				return available[i].Load < available[j].Load
			}
			return available[i].ID < available[j].ID
		})
		chosen := available[0]
		selected = append(selected, chosen.ID)
		existingSet[chosen.ID] = struct{}{}
		if chosen.FailureDomain != "" {
			selectedDomains[chosen.FailureDomain] = struct{}{}
		}
	}
	return selected
}

func availableStorageNodes(candidates []StorageNode, excluded map[string]struct{}) []StorageNode {
	out := make([]StorageNode, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := excluded[candidate.ID]; ok {
			continue
		}
		if candidate.Used >= candidate.Capacity {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func domainAlreadyUsed(domain string, used map[string]struct{}) bool {
	if domain == "" {
		return false
	}
	_, ok := used[domain]
	return ok
}

func comparePathWeightTie(a, b PathWeight) int {
	if cmp := bytes.Compare(pathHash(a.Path), pathHash(b.Path)); cmp != 0 {
		return cmp
	}
	if cmp := bytes.Compare(a.Path.Key, b.Path.Key); cmp != 0 {
		return cmp
	}
	return bytes.Compare(a.Path.PathNibbles, b.Path.PathNibbles)
}

func clonePathWeights(weights []PathWeight) []PathWeight {
	out := make([]PathWeight, len(weights))
	for i := range weights {
		out[i] = PathWeight{
			Path:         cloneStatePath(weights[i].Path),
			ReplicaCount: weights[i].ReplicaCount,
			Weight:       weights[i].Weight,
			Holders:      append([]string(nil), weights[i].Holders...),
		}
	}
	return out
}

func effectivePathKey(path StatePath, node PathNode) []byte {
	if len(node.PathKey) > 0 {
		return node.PathKey
	}
	return path.Key
}

func nodeReplicaKey(pathKey []byte, node PathNode) string {
	if len(node.Hash) > 0 {
		return hex.EncodeToString(node.Hash)
	}
	return fmt.Sprintf("%x:%d", pathKey, node.Position)
}

func nodePositionKey(pathKey []byte, node PathNode) string {
	return fmt.Sprintf("%x:%d:%s", pathKey, node.Position, nodeReplicaKey(pathKey, node))
}

func compareNodeWeights(a, b NodeWeight, weightDesc bool) int {
	if weightDesc && a.Weight != b.Weight {
		if a.Weight > b.Weight {
			return -1
		}
		return 1
	}
	if cmp := bytes.Compare(a.PathKey, b.PathKey); cmp != 0 {
		return cmp
	}
	if a.Node.Position != b.Node.Position {
		if a.Node.Position < b.Node.Position {
			return -1
		}
		return 1
	}
	return bytes.Compare(a.Node.Hash, b.Node.Hash)
}

func cloneNodeWeight(weight NodeWeight) NodeWeight {
	return NodeWeight{
		PathKey:      append([]byte(nil), weight.PathKey...),
		Node:         clonePathNode(weight.Node),
		ReplicaCount: weight.ReplicaCount,
		Weight:       weight.Weight,
		Holders:      append([]string(nil), weight.Holders...),
	}
}

func positionsForPath(path StatePath) map[int]int {
	out := make(map[int]int, len(path.Nodes))
	for i := range path.Nodes {
		out[path.Nodes[i].Position] = i
	}
	return out
}

func buildSuperNode(path StatePath, positions []int, partitions []Partition, weightByNode map[string]float64) SuperNode {
	posToIndex := positionsForPath(path)
	nodes := make([]PathNode, 0, len(positions))
	totalSize := 0
	totalWeight := 0.0
	for _, pos := range positions {
		idx := posToIndex[pos]
		node := clonePathNode(path.Nodes[idx])
		node.PathKey = append([]byte(nil), path.Key...)
		nodes = append(nodes, node)
		totalSize += node.Size
		totalWeight += weightByNode[nodePositionKey(path.Key, node)]
	}

	start := positions[0]
	parentPosition := -1
	var parentHash []byte
	if idx, ok := posToIndex[start]; ok {
		parentPosition = path.Nodes[idx].ParentPosition
		if parentIdx, ok := posToIndex[parentPosition]; ok {
			parentHash = append([]byte(nil), path.Nodes[parentIdx].Hash...)
		}
	}
	parentPartition := -1
	originalHolder := ""
	if id, ok := LocatePartitionForPath(partitions, path.Key); ok {
		parentPartition = id
		originalHolder = fmt.Sprintf("partition-%d", id)
	}

	return SuperNode{
		PathKey:         append([]byte(nil), path.Key...),
		StartPosition:   start,
		EndPosition:     positions[len(positions)-1],
		Nodes:           nodes,
		ParentHash:      parentHash,
		ParentPosition:  parentPosition,
		ParentPartition: parentPartition,
		OriginalHolder:  originalHolder,
		Weight:          totalWeight,
		TotalSize:       totalSize,
	}
}

func selectLowestLoadPartition(partitions []Partition) int {
	best := 0
	bestLoad := partitionLoad(partitions[0])
	for i := 1; i < len(partitions); i++ {
		load := partitionLoad(partitions[i])
		if load < bestLoad || (load == bestLoad && partitions[i].ID < partitions[best].ID) {
			best = i
			bestLoad = load
		}
	}
	return partitions[best].ID
}

func selectLowestLoadPartitionExcluding(partitions []Partition, excludedID int) (int, bool) {
	best := -1
	bestLoad := 0
	for i := range partitions {
		if partitions[i].ID == excludedID {
			continue
		}
		load := partitionLoad(partitions[i])
		if best == -1 || load < bestLoad || (load == bestLoad && partitions[i].ID < partitions[best].ID) {
			best = i
			bestLoad = load
		}
	}
	if best == -1 {
		return 0, false
	}
	return partitions[best].ID, true
}

func partitionLoad(partition Partition) int {
	total := 0
	for i := range partition.Paths {
		total += partition.Paths[i].Size
	}
	if total == 0 {
		return len(partition.Paths)
	}
	return total
}

func cloneSuperNodes(superNodes []SuperNode) []SuperNode {
	out := make([]SuperNode, len(superNodes))
	for i := range superNodes {
		out[i] = cloneSuperNode(superNodes[i])
	}
	return out
}

func cloneSuperNode(superNode SuperNode) SuperNode {
	out := SuperNode{
		PathKey:         append([]byte(nil), superNode.PathKey...),
		StartPosition:   superNode.StartPosition,
		EndPosition:     superNode.EndPosition,
		ParentHash:      append([]byte(nil), superNode.ParentHash...),
		ParentPosition:  superNode.ParentPosition,
		ParentPartition: superNode.ParentPartition,
		OriginalHolder:  superNode.OriginalHolder,
		Weight:          superNode.Weight,
		TotalSize:       superNode.TotalSize,
	}
	if superNode.Nodes != nil {
		out.Nodes = make([]PathNode, len(superNode.Nodes))
		for i := range superNode.Nodes {
			out.Nodes[i] = clonePathNode(superNode.Nodes[i])
		}
	}
	return out
}
