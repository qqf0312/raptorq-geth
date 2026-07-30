package fountainmptshadow

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const historySelectionDomain = "fountainmptshadow/history-selection/v1"

// PathResolver returns the original root-to-leaf MPT node encodings for key.
// Implementations may resolve them directly from retained trie roots; the
// processor does not retain a second copy of B_h,k.
type PathResolver interface {
	PathNodes(root common.Hash, key []byte) ([][]byte, error)
}

// Store is the sidecar persistence boundary. Production implementations can
// use a dedicated ethdb namespace without modifying trie node storage.
type Store interface {
	SaveRoots([]RootRecord) error
	SaveVersions([]VersionRecord) error
	SaveEncodedPath(*EncodedPath) error
	SaveHistoricalLeaf(*HistoricalLeaf) error
	SaveEpochAggregate(*EpochAggregate) error
}

type ProcessorConfig struct {
	EpochStart  uint64
	Seed        []byte
	MinimumRows int

	// OwnsHistorical is the local seed/partition decision. Nil stores every
	// historical leaf and is useful for single-node deployments and tests.
	OwnsHistorical func(VersionRecord) bool
}

type Processor struct {
	config  ProcessorConfig
	tracker *EpochTracker
	store   Store
}

func NewProcessor(config ProcessorConfig, store Store) (*Processor, error) {
	if len(config.Seed) == 0 {
		return nil, fmt.Errorf("empty processor seed")
	}
	if store == nil {
		store = NewMemoryStore()
	}
	return &Processor{
		config:  config,
		tracker: NewEpochTracker(config.EpochStart),
		store:   store,
	}, nil
}

func (p *Processor) RecordHeight(height uint64, root common.Hash, updates []KeyUpdate) error {
	return p.RecordHeightWithParent(height, root, common.Hash{}, updates)
}

func (p *Processor) RecordHeightWithParent(height uint64, root common.Hash, parent common.Hash, updates []KeyUpdate) error {
	if p == nil || p.tracker == nil {
		return fmt.Errorf("nil fountain MPT shadow processor")
	}
	return p.tracker.RecordHeightWithParent(height, root, parent, updates)
}

// Finalize builds final-path encoded records for finalKeys and historical leaf
// selector proofs for locally owned terminal versions. Source paths are
// resolved from retained roots and are not copied into the store.
func (p *Processor) Finalize(finalHeight uint64, finalKeys [][]byte, resolver PathResolver) (*FinalizeResult, error) {
	if p == nil || p.tracker == nil {
		return nil, fmt.Errorf("nil fountain MPT shadow processor")
	}
	if resolver == nil {
		return nil, fmt.Errorf("nil path resolver")
	}
	roots, versions, err := p.tracker.Finalize(finalHeight)
	if err != nil {
		return nil, err
	}
	if err := p.store.SaveRoots(roots); err != nil {
		return nil, fmt.Errorf("save roots: %w", err)
	}
	if err := p.store.SaveVersions(versions); err != nil {
		return nil, fmt.Errorf("save versions: %w", err)
	}
	finalRoot := roots[len(roots)-1].Root
	result := &FinalizeResult{
		FinalHeight:  finalHeight,
		FinalRoot:    finalRoot,
		RootCount:    len(roots),
		VersionCount: len(versions),
	}
	var (
		segments []aggregateSegment
		paths    []*EncodedPath
		leaves   []*HistoricalLeaf
	)
	for _, key := range uniqueKeys(finalKeys) {
		nodes, err := resolver.PathNodes(finalRoot, key)
		if err != nil {
			return nil, fmt.Errorf("resolve final path key %x: %w", key, err)
		}
		encoded, err := BuildEncodedPath(
			finalHeight,
			finalRoot,
			key,
			nodes,
			derivePathSeed(p.config.Seed, finalHeight, finalRoot, key),
			p.config.MinimumRows,
		)
		if err != nil {
			return nil, fmt.Errorf("encode final path key %x: %w", key, err)
		}
		files, _, err := FramePathNodes(nodes)
		if err != nil {
			return nil, fmt.Errorf("frame final path key %x: %w", key, err)
		}
		paths = append(paths, encoded)
		segments = append(segments, aggregateSegment{path: encoded, files: files})
		result.EncodedPathCount++
	}
	for _, version := range versions {
		if version.Final || !p.ownsHistorical(version) {
			continue
		}
		nodes, err := resolver.PathNodes(version.TerminalRoot, version.Key)
		if err != nil {
			return nil, fmt.Errorf("resolve historical path key %x height %d: %w", version.Key, version.TerminalHeight, err)
		}
		leaf, err := BuildHistoricalLeaf(
			version.StartHeight,
			version.TerminalHeight,
			version.TerminalRoot,
			version.Key,
			nodes,
		)
		if err != nil {
			return nil, fmt.Errorf("build historical leaf key %x height %d: %w", version.Key, version.TerminalHeight, err)
		}
		files, _, err := FramePathNodes(nodes)
		if err != nil {
			return nil, fmt.Errorf("frame historical path key %x height %d: %w", version.Key, version.TerminalHeight, err)
		}
		leaves = append(leaves, leaf)
		segments = append(segments, aggregateSegment{leaf: leaf, files: files})
		result.HistoricalLeafCount++
	}
	aggregate, err := buildEpochAggregate(finalHeight, segments)
	if err != nil {
		return nil, err
	}
	if aggregate != nil {
		aggregate.PathSeedBase = append([]byte(nil), p.config.Seed...)
	}
	for _, encoded := range paths {
		if err := p.store.SaveEncodedPath(encoded); err != nil {
			return nil, fmt.Errorf("save final path key %x: %w", encoded.Key, err)
		}
	}
	for _, leaf := range leaves {
		if err := p.store.SaveHistoricalLeaf(leaf); err != nil {
			return nil, fmt.Errorf("save historical leaf key %x height %d: %w", leaf.Key, leaf.TerminalHeight, err)
		}
	}
	if aggregate != nil {
		if err := p.store.SaveEpochAggregate(aggregate); err != nil {
			return nil, fmt.Errorf("save epoch aggregate: %w", err)
		}
		result.AggregateProofCount = 1
	}
	return result, nil
}

type FinalizeResult struct {
	FinalHeight uint64
	FinalRoot   common.Hash

	RootCount           int
	VersionCount        int
	EncodedPathCount    int
	HistoricalLeafCount int
	AggregateProofCount int
}

// SeedSelectsHistorical implements the sparse seed decision discussed by the
// storage protocol. A selectionModulus of r stores approximately 1/r records
// on one node. Deployment code must choose node IDs/replication so every leaf
// has the required number of owners.
func SeedSelectsHistorical(seed, nodeID, key []byte, terminalHeight uint64, selectionModulus uint64) bool {
	if selectionModulus <= 1 {
		return true
	}
	var height [8]byte
	binary.BigEndian.PutUint64(height[:], terminalHeight)
	digest := crypto.Keccak256(
		[]byte(historySelectionDomain),
		seed,
		nodeID,
		key,
		height[:],
	)
	value := binary.BigEndian.Uint64(digest[:8])
	return value%selectionModulus == 0
}

func (p *Processor) ownsHistorical(version VersionRecord) bool {
	if p.config.OwnsHistorical == nil {
		return true
	}
	return p.config.OwnsHistorical(version)
}

func derivePathSeed(seed []byte, height uint64, root common.Hash, key []byte) []byte {
	var encodedHeight [8]byte
	binary.BigEndian.PutUint64(encodedHeight[:], height)
	return crypto.Keccak256(seed, encodedHeight[:], root[:], key)
}

func uniqueKeys(keys [][]byte) [][]byte {
	seen := make(map[string][]byte)
	for _, key := range keys {
		if len(key) == 0 {
			continue
		}
		seen[string(key)] = append([]byte(nil), key...)
	}
	out := make([][]byte, 0, len(seen))
	for _, key := range seen {
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i]) < string(out[j]) })
	return out
}
