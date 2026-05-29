package partitioning

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
)

const (
	SortByKey  = "key"
	SortByHash = "hash"
)

// PathNode is one MPT node on a StatePath. First partitioning is
// StatePath-granular, while replica counting and risk scoring are
// PathNode-granular.
type PathNode struct {
	Hash []byte

	PathKey []byte

	// NodeType is optional trie metadata for tests/adapters that extract
	// StatePaths from an actual MPT-like tree.
	NodeType string
	// EdgeNibble is the nibble on the parent->child edge leading to this node.
	// The root uses 0xff.
	EdgeNibble byte
	// PrefixNibbles is the compact path fragment stored by an extension/leaf
	// node. Branch nodes usually leave it empty.
	PrefixNibbles []byte

	// Position is the node index in a StatePath, ordered root -> leaf.
	Position int

	// ParentPosition is the parent node index. The root has ParentPosition = -1.
	ParentPosition int

	// ChildCount is the number of effective child branches on this node.
	// SuperNode construction can merge upward through parents with ChildCount <= 1.
	ChildCount int

	Size int

	Raw any
}

// StatePath is one complete MPT state path, for example root -> branch ->
// extension -> leaf for one state key. First partitioning is StatePath-granular:
// the path is treated as one indivisible unit and is not split across
// partitions.
type StatePath struct {
	Key         []byte
	PathNibbles []byte
	NodeHashes  [][]byte
	Nodes       []PathNode
	LeafHash    []byte
	Size        int
	Raw         any
}

// Partition contains a contiguous range of the globally sorted StatePath list.
// StartIndex and EndIndex use the half-open interval [StartIndex, EndIndex).
// StartKey is the first path key in the partition. EndKey is the last path key
// contained in the partition, not an exclusive upper-bound key.
type Partition struct {
	ID int

	Paths []StatePath

	StartKey []byte
	EndKey   []byte

	StartIndex int
	EndIndex   int
}

type PartitionConfig struct {
	MinPartitions  int
	MaxPartitions  int
	BasePartitions int

	TargetBlockInterval int64
	CurrentBlockTime    int64
	PreviousBlockTime   int64

	SortBy string
}

// DeterminePartitionCount derives a deterministic partition count from recent
// block interval. The formula is:
//
//	ratio = TargetBlockInterval / deltaT
//	n = round(BasePartitions * ratio)
//	n = clamp(n, MinPartitions, MaxPartitions)
//
// Shorter intervals mean a more active system and increase partitions; longer
// intervals reduce partitions.
func DeterminePartitionCount(cfg PartitionConfig) (int, error) {
	if cfg.TargetBlockInterval <= 0 {
		return 0, fmt.Errorf("invalid TargetBlockInterval %d", cfg.TargetBlockInterval)
	}
	if cfg.BasePartitions <= 0 {
		return 0, fmt.Errorf("invalid BasePartitions %d", cfg.BasePartitions)
	}
	if cfg.MinPartitions <= 0 {
		return 0, fmt.Errorf("invalid MinPartitions %d", cfg.MinPartitions)
	}
	if cfg.MaxPartitions < cfg.MinPartitions {
		return 0, fmt.Errorf("MaxPartitions %d smaller than MinPartitions %d", cfg.MaxPartitions, cfg.MinPartitions)
	}
	deltaT := cfg.CurrentBlockTime - cfg.PreviousBlockTime
	if deltaT <= 0 {
		return 0, fmt.Errorf("invalid block interval %d", deltaT)
	}

	ratio := float64(cfg.TargetBlockInterval) / float64(deltaT)
	n := int(math.Round(float64(cfg.BasePartitions) * ratio))
	if n < cfg.MinPartitions {
		n = cfg.MinPartitions
	}
	if n > cfg.MaxPartitions {
		n = cfg.MaxPartitions
	}
	return n, nil
}

// SortStatePaths returns a new deterministically sorted slice. The input slice
// and the StatePath byte slices are not mutated.
func SortStatePaths(paths []StatePath, sortBy string) ([]StatePath, error) {
	if sortBy != SortByKey && sortBy != SortByHash {
		return nil, fmt.Errorf("unsupported sort mode %q", sortBy)
	}
	out := cloneStatePaths(paths)
	sort.SliceStable(out, func(i, j int) bool {
		return comparePaths(out[i], out[j], sortBy) < 0
	})
	return out, nil
}

// BuildPartitions sorts complete StatePath units and splits the sorted sequence
// into balanced contiguous partitions. No MPT node hash is split out of its
// StatePath.
func BuildPartitions(paths []StatePath, partitionCount int, sortBy string) ([]Partition, error) {
	if partitionCount <= 0 {
		return nil, fmt.Errorf("invalid partitionCount %d", partitionCount)
	}
	if len(paths) == 0 {
		return []Partition{}, nil
	}
	sorted, err := SortStatePaths(paths, sortBy)
	if err != nil {
		return nil, err
	}

	partitions := make([]Partition, partitionCount)
	base := len(sorted) / partitionCount
	extra := len(sorted) % partitionCount
	start := 0
	for i := 0; i < partitionCount; i++ {
		size := base
		if i < extra {
			size++
		}
		end := start + size
		part := Partition{
			ID:         i,
			StartIndex: start,
			EndIndex:   end,
			Paths:      cloneStatePaths(sorted[start:end]),
		}
		if size > 0 {
			part.StartKey = append([]byte(nil), sorted[start].Key...)
			part.EndKey = append([]byte(nil), sorted[end-1].Key...)
		}
		partitions[i] = part
		start = end
	}
	return partitions, nil
}

// LocatePartitionForPath finds the partition containing key. This first version
// intentionally scans complete StatePath entries so the lookup is independent
// of key-range edge semantics.
func LocatePartitionForPath(partitions []Partition, key []byte) (int, bool) {
	for i := range partitions {
		for j := range partitions[i].Paths {
			if bytes.Equal(partitions[i].Paths[j].Key, key) {
				return partitions[i].ID, true
			}
		}
	}
	return 0, false
}

func comparePaths(a, b StatePath, sortBy string) int {
	if sortBy == SortByKey {
		if cmp := bytes.Compare(a.Key, b.Key); cmp != 0 {
			return cmp
		}
		if cmp := bytes.Compare(pathHash(a), pathHash(b)); cmp != 0 {
			return cmp
		}
		return bytes.Compare(a.PathNibbles, b.PathNibbles)
	}
	if cmp := bytes.Compare(pathHash(a), pathHash(b)); cmp != 0 {
		return cmp
	}
	if cmp := bytes.Compare(a.Key, b.Key); cmp != 0 {
		return cmp
	}
	return bytes.Compare(a.PathNibbles, b.PathNibbles)
}

func pathHash(path StatePath) []byte {
	if len(path.LeafHash) > 0 {
		return path.LeafHash
	}
	h := sha256.New()
	if len(path.NodeHashes) > 0 {
		for i := range path.NodeHashes {
			h.Write(path.NodeHashes[i])
			h.Write([]byte{0})
		}
	} else {
		h.Write(path.Key)
	}
	sum := h.Sum(nil)
	return sum
}

func replicaMapKey(path StatePath) string {
	if len(path.LeafHash) > 0 {
		return hex.EncodeToString(path.LeafHash)
	}
	return hex.EncodeToString(path.Key)
}

func cloneStatePaths(paths []StatePath) []StatePath {
	out := make([]StatePath, len(paths))
	for i := range paths {
		out[i] = cloneStatePath(paths[i])
	}
	return out
}

func cloneStatePath(path StatePath) StatePath {
	out := StatePath{
		Key:         append([]byte(nil), path.Key...),
		PathNibbles: append([]byte(nil), path.PathNibbles...),
		LeafHash:    append([]byte(nil), path.LeafHash...),
		Size:        path.Size,
		Raw:         path.Raw,
	}
	if path.NodeHashes != nil {
		out.NodeHashes = make([][]byte, len(path.NodeHashes))
		for i := range path.NodeHashes {
			out.NodeHashes[i] = append([]byte(nil), path.NodeHashes[i]...)
		}
	}
	if path.Nodes != nil {
		out.Nodes = make([]PathNode, len(path.Nodes))
		for i := range path.Nodes {
			out.Nodes[i] = clonePathNode(path.Nodes[i])
		}
	}
	return out
}

func clonePathNode(node PathNode) PathNode {
	return PathNode{
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

func validateSortBy(sortBy string) error {
	if sortBy != SortByKey && sortBy != SortByHash {
		return errors.New("sortBy must be \"key\" or \"hash\"")
	}
	return nil
}
