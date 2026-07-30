package fountainmptshadow

import (
	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/internal/fileipa"
)

const (
	// SourceChunkSize is the lossless byte width used by fileipa for one Fr
	// source chunk.
	SourceChunkSize = 31
	// SourceSymbolSize groups eight Fr chunks into one compact fountain source
	// symbol. Only the final symbol is zero-padded.
	SourceSymbolSize = 8 * SourceChunkSize

	latestPathDomain     = "fountainmptshadow/latest-path/v1"
	historicalLeafDomain = "fountainmptshadow/historical-leaf/v1"
	epochAggregateDomain = "fountainmptshadow/epoch-aggregate/v1"
)

// PathLayout describes the compact path stream supplied to fileipa.
type PathLayout struct {
	NodeCount    uint64
	SourceCount  uint64
	FileSize     uint64
	CompactBytes uint64
}

// EncodedPath is the sidecar output for one original MPT path. Matrix is the
// source-file coefficient matrix, Blocks contains one chunk vector per row,
// and Proof folds every (matrix row, block chunk) statement over the shared
// original-path commitment Q.
type EncodedPath struct {
	Height uint64
	Root   common.Hash
	Key    []byte
	Layout PathLayout

	Seed   []byte
	Matrix [][]fr.Element
	Blocks [][]fr.Element

	Q     bn254.G1Affine
	Proof *fileipa.FoldedFileIPAResult

	Aggregate      *EpochAggregate
	AggregateStart uint64
}

// HistoricalLeaf is the special one-row encoding [0,...,0,1] for the leaf in
// an original path at the version's terminal root.
type HistoricalLeaf struct {
	StartHeight    uint64
	TerminalHeight uint64
	TerminalRoot   common.Hash
	Key            []byte
	Layout         PathLayout

	// Leaf is the canonical raw MPT leaf RLP retained on disk. Selector marks
	// its trailing compact path chunks; each marked chunk becomes one IPA row.
	Leaf     []byte
	Selector []fr.Element
	Block    []fr.Element

	Q     bn254.G1Affine
	Proof *fileipa.FoldedFileIPAResult

	Aggregate      *EpochAggregate
	AggregateIndex uint64
}

const (
	AggregateRelationPath uint8 = iota
	AggregateRelationHistoricalLeaf
)

// AggregateRelation identifies one public row inside an epoch aggregate.
// Offset and Length refer to the relation's segment in the temporary direct-sum
// witness. Path relations are stored in row-major chunk order; historical
// relations are stored in leaf-chunk order, so no chunk coordinate is stored.
// The witness itself is never persisted.
type AggregateRelation struct {
	Kind           uint8
	Root           common.Hash
	Key            []byte
	NodeCount      uint64
	CompactBytes   uint64
	StartHeight    uint64
	TerminalHeight uint64
	Row            uint64
	Offset         uint64
	Length         uint64
	C              fr.Element
	Seed           []byte
	SourceCount    uint64
	RowCount       uint64
	SelectStart    uint64
	SelectCount    uint64
}

type AggregateSegmentCommitment struct {
	Offset uint64
	Q      bn254.G1Affine
}

// EpochAggregate is the one-proof bundle shared by every locally owned
// relation in one epoch. Its verifier reconstructs Q as the sum of the
// persisted segment commitments.
type EpochAggregate struct {
	Height uint64
	// PathSeedBase is the per-epoch seed from which every latest-path seed is
	// derived. It is persisted once for the aggregate, never per relation.
	PathSeedBase []byte
	Relations    []AggregateRelation
	Segments     []AggregateSegmentCommitment
	Proof        *fileipa.FoldedFileIPAResult
}

// RootRecord retains every height/root pair even after source trie nodes are
// pruned.
type RootRecord struct {
	Height uint64
	Root   common.Hash
}

// VersionRecord describes the validity interval of one key version. A version
// ending because of an update at height h has TerminalHeight h-1.
type VersionRecord struct {
	Key            []byte
	StartHeight    uint64
	TerminalHeight uint64
	TerminalRoot   common.Hash
	Final          bool
}
