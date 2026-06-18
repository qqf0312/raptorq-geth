package partitionedmptshadow

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/internal/mptagg"
	"github.com/ethereum/go-ethereum/internal/partitioning"
)

type rawNodeSetMetadataStore interface {
	SaveRawNodeSetWithMetadata(root common.Hash, partitionID int, rawSet map[string]bool, nodeSizes map[string]int) error
}

type Processor struct {
	manager       *partitioning.MPTPartitionManager
	store         Store
	opts          mptagg.CompressOptions
	nodePartition int
}

func NewProcessor(manager *partitioning.MPTPartitionManager, store Store, opts mptagg.CompressOptions) (*Processor, error) {
	if manager == nil {
		return nil, fmt.Errorf("nil partition manager")
	}
	if store == nil {
		store = NewMemoryStore()
	}
	if opts.Params == nil {
		return nil, fmt.Errorf("nil compression params")
	}
	return &Processor{manager: manager, store: store, opts: opts, nodePartition: -1}, nil
}

func (p *Processor) SetNodePartition(partitionID int) {
	if p == nil {
		return
	}
	p.nodePartition = partitionID
}

func (p *Processor) OnRootUpdate(input Input) (*Result, error) {
	if p == nil {
		return nil, fmt.Errorf("nil shadow processor")
	}
	if input.View == nil {
		return nil, fmt.Errorf("nil root view")
	}
	if len(input.ChangedPaths) == 0 {
		return nil, nil
	}
	result, err := p.addIncrementalRoot(input.View, input.ChangedPaths)
	if err != nil {
		return nil, err
	}
	root := input.Root
	if root == (common.Hash{}) {
		root = common.HexToHash(result.RootHash)
	}
	out := &Result{
		Root:           root,
		Parent:         input.Parent,
		Block:          input.Block,
		DirtyNodeStats: input.DirtyNodeStats,

		PartitionResult: result,
		LocalViews:      make(map[int]*mptagg.LocalPartitionView, len(result.Partitions)),
	}
	for partitionID, rawSet := range result.RawNodeSets {
		if !p.storesPartition(partitionID) {
			continue
		}
		localOwned := mergeRawNodeSets(p.store.HistoricalRawNodeSet(partitionID), rawSet)
		view, err := mptagg.BuildLocalPartitionViewFromRawNodeSet(result.LatestPaths, localOwned, partitionID, p.opts)
		if err != nil {
			return nil, fmt.Errorf("build local view partition-%d: %w", partitionID, err)
		}
		if metaStore, ok := p.store.(rawNodeSetMetadataStore); ok {
			if err := metaStore.SaveRawNodeSetWithMetadata(root, partitionID, rawSet, rawNodeSizesForSet(result.LatestPaths, rawSet)); err != nil {
				return nil, fmt.Errorf("save raw node set partition-%d: %w", partitionID, err)
			}
		} else if err := p.store.SaveRawNodeSet(root, partitionID, rawSet); err != nil {
			return nil, fmt.Errorf("save raw node set partition-%d: %w", partitionID, err)
		}
		out.LocalViews[partitionID] = view
	}
	if err := p.store.SaveResult(out); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *Processor) storesPartition(partitionID int) bool {
	return p.nodePartition < 0 || p.nodePartition == partitionID
}

func (p *Processor) Store() Store {
	if p == nil {
		return nil
	}
	return p.store
}

func (p *Processor) addIncrementalRoot(root RootView, changedPaths []partitioning.StatePath) (*partitioning.MPTPartitionResult, error) {
	latestPaths, err := root.ExtractStatePaths()
	if err != nil {
		return nil, err
	}
	previousRootHash := p.manager.CurrentRootHash
	previousPaths := append([]partitioning.StatePath(nil), p.manager.CurrentPaths...)
	diff := partitioning.DiffStatePaths(previousPaths, latestPaths)
	diff.Changed = append([]partitioning.StatePath(nil), changedPaths...)

	sortBy := p.manager.SortBy
	if sortBy == "" {
		sortBy = partitioning.SortByKey
	}
	partitions, err := partitioning.BuildPartitions(diff.Changed, p.manager.PartitionCount, sortBy)
	if err != nil {
		return nil, err
	}
	plans, high, superNodes, err := partitioning.OptimizeSuperNodeDistribution(diff.Changed, partitions, p.manager.Config)
	if err != nil {
		return nil, err
	}
	latestReachable := partitioning.BuildLatestReachableNodeHashSet(latestPaths)
	rawNodeSets := partitioning.BuildRawNodeSets(partitions, plans, latestReachable, preserveLatestEnabled(p.manager))

	p.manager.CurrentRootHash = root.RootHashString()
	p.manager.CurrentPaths = latestPaths
	p.manager.Partitions = partitions
	p.manager.Plans = plans
	p.manager.HighNodes = high
	p.manager.SuperNodes = superNodes
	p.manager.RawNodeSets = rawNodeSets

	return &partitioning.MPTPartitionResult{
		RootHash:         p.manager.CurrentRootHash,
		PreviousRootHash: previousRootHash,
		LatestPaths:      latestPaths,
		PreviousPaths:    previousPaths,
		Diff:             diff,
		Partitions:       partitions,
		Plans:            plans,
		HighNodes:        high,
		SuperNodes:       superNodes,
		LatestReachable:  latestReachable,
		RawNodeSets:      rawNodeSets,
	}, nil
}

func preserveLatestEnabled(manager *partitioning.MPTPartitionManager) bool {
	if manager.PreserveLatestSet {
		return manager.PreserveLatest
	}
	return true
}

func mergeRawNodeSets(sets ...map[string]bool) map[string]bool {
	out := make(map[string]bool)
	for _, set := range sets {
		for hash, keep := range set {
			if keep {
				out[hash] = true
			}
		}
	}
	return out
}

func rawNodeSizesForSet(paths []partitioning.StatePath, rawSet map[string]bool) map[string]int {
	sizes := make(map[string]int)
	for i := range paths {
		for j := range paths[i].Nodes {
			node := paths[i].Nodes[j]
			if len(node.Hash) == 0 || node.Size <= 0 {
				continue
			}
			hash := normalizeStoreRawHashKey(common.BytesToHash(node.Hash).Hex())
			if !rawSet[hash] && !rawSet["0x"+hash] {
				continue
			}
			sizes[hash] = node.Size
		}
	}
	return sizes
}
