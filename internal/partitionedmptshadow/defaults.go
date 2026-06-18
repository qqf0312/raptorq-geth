package partitionedmptshadow

import (
	"fmt"

	"github.com/ethereum/go-ethereum/internal/ipa"
	"github.com/ethereum/go-ethereum/internal/mptagg"
	"github.com/ethereum/go-ethereum/internal/partitioning"
)

const defaultIPAVectorSize = 4096

// NewDefaultTrieDatabaseAdapter builds an in-memory shadow adapter suitable for
// local node experiments.
func NewDefaultTrieDatabaseAdapter(partitionCount int) (*TrieDatabaseAdapter, error) {
	return NewDefaultTrieDatabaseAdapterWithStore(partitionCount, NewMemoryStore())
}

func NewDefaultTrieDatabaseAdapterWithStore(partitionCount int, store Store) (*TrieDatabaseAdapter, error) {
	return NewDefaultTrieDatabaseAdapterWithStoreAndNodePartition(partitionCount, -1, store)
}

func NewDefaultTrieDatabaseAdapterWithStoreAndNodePartition(partitionCount int, nodePartition int, store Store) (*TrieDatabaseAdapter, error) {
	if partitionCount <= 0 {
		partitionCount = 4
	}
	if nodePartition >= partitionCount {
		return nil, fmt.Errorf("node partition %d out of range [0, %d)", nodePartition, partitionCount)
	}
	params, err := ipa.NewTestParams(defaultIPAVectorSize)
	if err != nil {
		return nil, fmt.Errorf("new shadow IPA params: %w", err)
	}
	manager := &partitioning.MPTPartitionManager{
		PartitionCount: partitionCount,
		SortBy:         partitioning.SortByKey,
		Config: partitioning.OptimizationConfig{
			Beta:               1.2,
			HighRiskMultiplier: 1.1,
			MaxSuperNodeLength: 3,
		},
		PreserveLatest:    false,
		PreserveLatestSet: true,
	}
	processor, err := NewProcessor(manager, store, mptagg.CompressOptions{Params: params})
	if err != nil {
		return nil, err
	}
	processor.SetNodePartition(nodePartition)
	return &TrieDatabaseAdapter{Processor: processor}, nil
}
