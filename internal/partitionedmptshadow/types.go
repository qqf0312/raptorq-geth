package partitionedmptshadow

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/internal/mptagg"
	"github.com/ethereum/go-ethereum/internal/partitioning"
)

// RootView is the minimal current-root surface needed by the shadow pipeline.
// Test code can pass *mpttest.TestMPT; production code can pass an adapter over
// root + trie database + dirty nodes.
type RootView interface {
	partitioning.MPTStatePathRoot
}

// Input describes one shadow account-trie update round. ChangedPaths is the
// minimal partition input for this round; RootView still exposes the full
// current root so local compression can traverse the complete MPT.
type Input struct {
	Root           common.Hash
	Parent         common.Hash
	Block          uint64
	View           RootView
	ChangedPaths   []partitioning.StatePath
	DirtyNodeStats DirtyNodeStats
}

// Result is the sidecar output for one shadow round.
type Result struct {
	Root           common.Hash
	Parent         common.Hash
	Block          uint64
	DirtyNodeStats DirtyNodeStats

	PartitionResult *partitioning.MPTPartitionResult
	LocalViews      map[int]*mptagg.LocalPartitionView
}

type DirtyNodeStats struct {
	NodeCount        uint64
	NodeBytes        uint64
	AccountNodeCount uint64
	AccountNodeBytes uint64
	StorageNodeCount uint64
	StorageNodeBytes uint64
	DeletedNodeCount uint64
}
