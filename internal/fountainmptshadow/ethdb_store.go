package fountainmptshadow

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/internal/fileipa"
	"github.com/ethereum/go-ethereum/internal/folding"
	"github.com/ethereum/go-ethereum/internal/ipa"
	"github.com/ethereum/go-ethereum/internal/mptproofmsg"
	"github.com/ethereum/go-ethereum/rlp"
)

var (
	rootPrefix       = []byte{0xf0}
	versionPrefix    = []byte{0xf1}
	pathPrefix       = []byte{0xf2}
	historicalPrefix = []byte{0xf3}
	aggregatePrefix  = []byte{0xf4}
	nodeStatsPrefix  = []byte{0xf5}
)

type EthDBStore struct {
	db             ethdb.Database
	rootResolver   func(uint64) (common.Hash, bool)
	aggregateMu    sync.Mutex
	aggregateCache map[aggregateCacheKey]*EpochAggregate
}

type aggregateCacheKey struct {
	height uint64
	id     common.Hash
}

type AuditPath struct {
	Height   uint64
	Root     common.Hash
	Key      []byte
	Rows     int
	Verified bool
}

type AuditHistoricalLeaf struct {
	StartHeight    uint64
	TerminalHeight uint64
	TerminalRoot   common.Hash
	Key            []byte
	Verified       bool
}

type AuditSummary struct {
	Roots            int
	Versions         int
	Paths            []AuditPath
	HistoricalLeaves []AuditHistoricalLeaf
	AggregateProofs  int
}

// NodeUpdateStats is the storage written by the unpartitioned MPT baseline at
// one height. BlobBytes counts canonical RLP node bytes and HashBytes counts
// the 32-byte hash keys used to address non-deleted nodes.
type NodeUpdateStats struct {
	Height    uint64
	Nodes     uint64
	BlobBytes uint64
	HashBytes uint64
	Deletes   uint64
}

// StorageBreakdown separates protocol payload from record/index metadata.
// RecordBytes is the exact sum of persisted key and value lengths, excluding
// LevelDB's own tables, logs, indexes, compression, and filesystem allocation.
type StorageBreakdown struct {
	Baseline NodeUpdateStats

	EncodedBlocksBytes       uint64
	HistoricalLeafBytes      uint64
	GenerationMatrixBytes    uint64
	CommitmentBytes          uint64
	ProofBytes               uint64
	ProofStatementBytes      uint64
	ProofFoldChallengeBytes  uint64
	ProofIPABytes            uint64
	RootRecordBytes          uint64
	VersionIndexBytes        uint64
	OtherRecordMetadataBytes uint64
	RecordBytes              uint64
}

func NewEthDBStore(db ethdb.Database) *EthDBStore {
	return &EthDBStore{db: db, aggregateCache: make(map[aggregateCacheKey]*EpochAggregate)}
}

// NewEthDBStoreWithRootResolver reuses roots already retained by the canonical
// chain database instead of duplicating them in the fountain sidecar.
func NewEthDBStoreWithRootResolver(db ethdb.Database, resolver func(uint64) (common.Hash, bool)) *EthDBStore {
	store := NewEthDBStore(db)
	store.rootResolver = resolver
	return store
}

type storedVersion struct {
	Key []byte

	TerminalHeight uint64
	Final          bool
}

type storedEncodedPath struct {
	Height uint64
	Root   common.Hash
	Key    []byte

	NodeCount       uint64
	SourceCount     uint64
	CompactBytes    uint64
	Seed            []byte
	Blocks          [][]mptproofmsg.ScalarWire
	Aggregated      bool
	AggregateHeight uint64
	AggregateID     common.Hash
	AggregateStart  uint64

	Q              mptproofmsg.G1Wire
	RowCs          []mptproofmsg.ScalarWire
	FoldChallenges [][]mptproofmsg.ScalarWire
	IPAProof       mptproofmsg.IPAProofWire
}

type storedHistoricalLeaf struct {
	StartHeight    uint64
	TerminalHeight uint64
	TerminalRoot   common.Hash
	Key            []byte

	NodeCount       uint64
	SourceCount     uint64
	CompactBytes    uint64
	Leaf            []byte
	Aggregated      bool
	AggregateHeight uint64
	AggregateID     common.Hash
	AggregateIndex  uint64

	Q              mptproofmsg.G1Wire
	RowCs          []mptproofmsg.ScalarWire
	FoldChallenges [][]mptproofmsg.ScalarWire
	IPAProof       mptproofmsg.IPAProofWire
}

type storedAggregateRelation struct {
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
	Seed           []byte
	SourceCount    uint64
	RowCount       uint64
	SelectStart    uint64
	SelectCount    uint64
}

type storedAggregateSegmentCommitment struct {
	Offset uint64
	Q      mptproofmsg.G1Wire
}

type storedEpochAggregate struct {
	Height         uint64
	PathRoot       common.Hash
	PathSeedBase   []byte
	Relations      []storedAggregateRelation
	Segments       []storedAggregateSegmentCommitment
	RowCs          []mptproofmsg.ScalarWire
	FoldChallenges [][]mptproofmsg.ScalarWire
	IPAProof       mptproofmsg.IPAProofWire
}

func (s *EthDBStore) SaveRoots(roots []RootRecord) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("nil fountain MPT ethdb store")
	}
	if s.rootResolver != nil {
		return nil
	}
	for _, record := range roots {
		blob, err := rlp.EncodeToBytes(record.Root)
		if err != nil {
			return err
		}
		if err := s.db.Put(rootKey(record.Height), blob); err != nil {
			return err
		}
	}
	return nil
}

func (s *EthDBStore) SaveVersions(versions []VersionRecord) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("nil fountain MPT ethdb store")
	}
	for _, version := range versions {
		stored := storedVersion{
			TerminalHeight: version.TerminalHeight,
			Final:          version.Final,
		}
		blob, err := rlp.EncodeToBytes(stored)
		if err != nil {
			return err
		}
		if err := s.db.Put(versionKey(version.TerminalRoot, version.Key, version.StartHeight), blob); err != nil {
			return err
		}
	}
	return nil
}

func (s *EthDBStore) SaveEncodedPath(path *EncodedPath) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("nil fountain MPT ethdb store")
	}
	if err := validateEncodedPath(path); err != nil {
		return err
	}
	stored := storedEncodedPath{
		Height:       path.Height,
		NodeCount:    path.Layout.NodeCount,
		CompactBytes: path.Layout.CompactBytes,
		Aggregated:   path.Aggregate != nil,
	}
	if path.Aggregate != nil {
		stored.AggregateHeight = path.Aggregate.Height
		stored.AggregateID = epochAggregateID(path.Aggregate)
	} else {
		stored.Root = path.Root
		stored.Key = append([]byte(nil), path.Key...)
		stored.SourceCount = path.Layout.SourceCount
		stored.Seed = append([]byte(nil), path.Seed...)
		stored.AggregateStart = path.AggregateStart
		stored.Blocks = scalarBlocksToWire(path.Blocks)
		stored.Q = mptproofmsg.G1ToWire(path.Q)
		proof, err := mptproofmsg.IPAProofToWire(path.Proof.IPAProof)
		if err != nil {
			return err
		}
		stored.RowCs = make([]mptproofmsg.ScalarWire, len(path.Proof.Rows))
		for i := range path.Proof.Rows {
			stored.RowCs[i] = mptproofmsg.ScalarToWire(path.Proof.Rows[i].C)
		}
		stored.IPAProof = proof
	}
	var blob []byte
	if stored.Aggregated {
		blob = encodeCompactPath(stored)
	} else {
		var err error
		blob, err = rlp.EncodeToBytes(stored)
		if err != nil {
			return err
		}
	}
	return s.db.Put(pathKey(path.Root, path.Key), blob)
}

func (s *EthDBStore) SaveHistoricalLeaf(leaf *HistoricalLeaf) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("nil fountain MPT ethdb store")
	}
	if err := validateHistoricalLeaf(leaf); err != nil {
		return err
	}
	stored := storedHistoricalLeaf{
		StartHeight:    leaf.StartHeight,
		TerminalHeight: leaf.TerminalHeight,
		NodeCount:      leaf.Layout.NodeCount,
		CompactBytes:   leaf.Layout.CompactBytes,
		Leaf:           append([]byte(nil), leaf.Leaf...),
		Aggregated:     leaf.Aggregate != nil,
	}
	if leaf.Aggregate != nil {
		stored.AggregateHeight = leaf.Aggregate.Height
		stored.AggregateID = epochAggregateID(leaf.Aggregate)
	} else {
		stored.TerminalRoot = leaf.TerminalRoot
		stored.Key = append([]byte(nil), leaf.Key...)
		stored.SourceCount = leaf.Layout.SourceCount
		stored.AggregateIndex = leaf.AggregateIndex
		stored.Q = mptproofmsg.G1ToWire(leaf.Q)
		proof, err := mptproofmsg.IPAProofToWire(leaf.Proof.IPAProof)
		if err != nil {
			return err
		}
		stored.RowCs = make([]mptproofmsg.ScalarWire, len(leaf.Proof.Rows))
		for i := range leaf.Proof.Rows {
			stored.RowCs[i] = mptproofmsg.ScalarToWire(leaf.Proof.Rows[i].C)
		}
		stored.IPAProof = proof
	}
	var blob []byte
	if stored.Aggregated {
		blob = encodeCompactHistory(stored)
	} else {
		var err error
		blob, err = rlp.EncodeToBytes(stored)
		if err != nil {
			return err
		}
	}
	return s.db.Put(historicalKey(leaf.TerminalRoot, leaf.Key), blob)
}

func (s *EthDBStore) SaveEpochAggregate(aggregate *EpochAggregate) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("nil fountain MPT ethdb store")
	}
	ok, err := VerifyEpochAggregate(aggregate)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("invalid epoch aggregate proof")
	}
	proof, err := mptproofmsg.IPAProofToWire(aggregate.Proof.IPAProof)
	if err != nil {
		return err
	}
	pathRelations := 0
	for i := range aggregate.Relations {
		if aggregate.Relations[i].Kind == AggregateRelationPath {
			pathRelations++
		}
	}
	compactRelations, err := compactAggregateRelations(aggregate.Relations)
	if err != nil {
		return err
	}
	pathRoot, sharedPathRoot := aggregatePathRoot(aggregate.Relations)
	for i := range compactRelations {
		relation := &compactRelations[i]
		relation.Offset = 0
		if relation.Kind != AggregateRelationPath {
			continue
		}
		if sharedPathRoot {
			relation.Root = common.Hash{}
		}
		if len(aggregate.PathSeedBase) != 0 {
			seedRoot := relation.Root
			if sharedPathRoot {
				seedRoot = pathRoot
			}
			expected := derivePathSeed(
				aggregate.PathSeedBase, aggregate.Height, seedRoot, relation.Key,
			)
			if !bytes.Equal(expected, relation.Seed) {
				return fmt.Errorf("aggregate path seed is not derived from the epoch seed")
			}
			relation.Seed = nil
		}
		relation.SourceCount = 0
		relation.Row = 0
		relation.RowCount = 0
	}
	stored := storedEpochAggregate{
		Height:       aggregate.Height,
		PathSeedBase: append([]byte(nil), aggregate.PathSeedBase...),
		Relations:    compactRelations,
		Segments:     make([]storedAggregateSegmentCommitment, len(aggregate.Segments)),
		RowCs:        make([]mptproofmsg.ScalarWire, pathRelations),
		IPAProof:     proof,
	}
	if sharedPathRoot {
		stored.PathRoot = pathRoot
	}
	for i := range aggregate.Segments {
		stored.Segments[i] = storedAggregateSegmentCommitment{
			Q: mptproofmsg.G1ToWire(aggregate.Segments[i].Q),
		}
	}
	nextPathC := 0
	for i := range aggregate.Relations {
		if aggregate.Relations[i].Kind == AggregateRelationPath {
			stored.RowCs[nextPathC] = mptproofmsg.ScalarToWire(aggregate.Proof.Rows[i].C)
			nextPathC++
		}
	}
	blob := encodeCompactAggregate(stored)
	id := epochAggregateID(aggregate)
	if err := s.db.Put(aggregateKey(aggregate.Height, id), blob); err != nil {
		return err
	}
	s.aggregateMu.Lock()
	s.aggregateCache[aggregateCacheKey{height: aggregate.Height, id: id}] = aggregate
	s.aggregateMu.Unlock()
	return nil
}

// SaveNodeUpdateStats records the baseline MPT write set for one height.
func (s *EthDBStore) SaveNodeUpdateStats(stats NodeUpdateStats) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("nil fountain MPT ethdb store")
	}
	blob, err := rlp.EncodeToBytes(stats)
	if err != nil {
		return err
	}
	return s.db.Put(nodeStatsKey(stats.Height), blob)
}

func (s *EthDBStore) Root(height uint64) (common.Hash, bool, error) {
	if s != nil && s.rootResolver != nil {
		root, ok := s.rootResolver(height)
		return root, ok, nil
	}
	blob, err := s.get(rootKey(height))
	if err != nil || blob == nil {
		return common.Hash{}, false, err
	}
	var root common.Hash
	if err := rlp.DecodeBytes(blob, &root); err != nil {
		return common.Hash{}, false, err
	}
	return root, true, nil
}

func (s *EthDBStore) EncodedPath(root common.Hash, key []byte) (*EncodedPath, bool, error) {
	blob, err := s.get(pathKey(root, key))
	if err != nil || blob == nil {
		return nil, false, err
	}
	var stored storedEncodedPath
	if len(blob) > 0 && blob[0] == compactPathVersion {
		stored, err = decodeCompactPath(blob)
	} else {
		err = rlp.DecodeBytes(blob, &stored)
	}
	if err != nil {
		return nil, false, err
	}
	if stored.Aggregated {
		return s.aggregatedEncodedPath(stored, root, key)
	}
	if stored.SourceCount == 0 {
		return nil, false, fmt.Errorf("empty encoded path source layout")
	}
	blocks, err := scalarBlocksFromWire(stored.Blocks)
	if err != nil {
		return nil, false, err
	}
	matrix, err := GenerateMatrix(stored.Seed, int(stored.SourceCount), len(blocks))
	if err != nil {
		return nil, false, err
	}
	chunkCount := 0
	if len(blocks) > 0 {
		chunkCount = len(blocks[0])
	}
	relationCount := len(matrix) * chunkCount
	if len(matrix) != len(blocks) ||
		len(stored.RowCs) < relationCount || len(stored.RowCs) != nextPowerOfTwo(len(stored.RowCs)) {
		return nil, false, fmt.Errorf("stored row count does not match regenerated matrix")
	}
	out := &EncodedPath{
		Height: stored.Height,
		Root:   stored.Root,
		Key:    append([]byte(nil), stored.Key...),
		Layout: PathLayout{
			NodeCount:    stored.NodeCount,
			SourceCount:  stored.SourceCount,
			FileSize:     SourceSymbolSize,
			CompactBytes: stored.CompactBytes,
		},
		Seed:           append([]byte(nil), stored.Seed...),
		Matrix:         matrix,
		Blocks:         blocks,
		AggregateStart: stored.AggregateStart,
	}
	q, err := mptproofmsg.G1FromWire(stored.Q)
	if err != nil {
		return nil, false, err
	}
	out.Q = q
	vectorLength := nextPowerOfTwo(int(stored.SourceCount) * (SourceSymbolSize / SourceChunkSize))
	params, err := ipa.NewTestParams(vectorLength)
	if err != nil {
		return nil, false, err
	}
	rows := make([]fileipa.FileIPARow, len(stored.RowCs))
	chunksPerFile := SourceSymbolSize / SourceChunkSize
	for i := range stored.RowCs {
		c, err := mptproofmsg.ScalarFromWire(stored.RowCs[i])
		if err != nil {
			return nil, false, err
		}
		a := make([]fr.Element, vectorLength)
		if i < relationCount {
			row := i / chunksPerFile
			chunk := i % chunksPerFile
			a, err = expandCoefficientChunkRow(matrix[row], chunksPerFile, chunk, vectorLength)
			if err != nil {
				return nil, false, err
			}
		} else if !c.IsZero() {
			return nil, false, fmt.Errorf("stored padding row %d has non-zero C", i)
		}
		rows[i] = fileipa.FileIPARow{A: a, C: c}
	}
	challenges, err := loadOrDeriveFoldChallenges(stored.FoldChallenges, rows, q, latestPathDomain)
	if err != nil {
		return nil, false, err
	}
	proof, err := mptproofmsg.IPAProofFromWire(stored.IPAProof)
	if err != nil {
		return nil, false, err
	}
	out.Proof = &fileipa.FoldedFileIPAResult{
		Params:    params,
		Rows:      rows,
		FoldProof: &fileipa.FileFoldProof{Challenges: challenges},
		IPAProof:  proof,
	}
	if err := validateEncodedPath(out); err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func (s *EthDBStore) aggregatedEncodedPath(
	stored storedEncodedPath,
	root common.Hash,
	key []byte,
) (*EncodedPath, bool, error) {
	aggregate, err := s.epochAggregate(stored.AggregateHeight, stored.AggregateID)
	if err != nil {
		return nil, false, err
	}
	start := -1
	for i := range aggregate.Relations {
		relation := aggregate.Relations[i]
		if relation.Kind == AggregateRelationPath && relation.Root == root &&
			bytes.Equal(relation.Key, key) && relation.Row == 0 {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, false, fmt.Errorf("aggregate path relation not found")
	}
	first := aggregate.Relations[start]
	if first.RowCount == 0 {
		return nil, false, fmt.Errorf("aggregate path relation metadata mismatch")
	}
	chunksPerBlock := SourceSymbolSize / SourceChunkSize
	relationCount := int(first.RowCount) * chunksPerBlock
	if relationCount/chunksPerBlock != int(first.RowCount) ||
		start+relationCount > len(aggregate.Relations) {
		return nil, false, fmt.Errorf("aggregate path relation range is out of bounds")
	}
	matrix, err := GenerateMatrix(first.Seed, int(first.SourceCount), int(first.RowCount))
	if err != nil {
		return nil, false, err
	}
	blocks := make([][]fr.Element, len(matrix))
	for row := range matrix {
		blocks[row] = make([]fr.Element, chunksPerBlock)
		for chunk := 0; chunk < chunksPerBlock; chunk++ {
			relation := aggregate.Relations[start+row*chunksPerBlock+chunk]
			if relation.Kind != AggregateRelationPath || relation.Root != root ||
				!bytes.Equal(relation.Key, key) || relation.Row != uint64(row) ||
				relation.Offset != first.Offset || relation.Length != first.Length ||
				relation.SourceCount != first.SourceCount || relation.RowCount != first.RowCount ||
				!bytes.Equal(relation.Seed, first.Seed) {
				return nil, false, fmt.Errorf("aggregate path relation %d is not canonical", row*chunksPerBlock+chunk)
			}
			blocks[row][chunk] = relation.C
		}
	}
	q, ok := aggregateSegmentQ(aggregate, first.Offset)
	if !ok {
		return nil, false, fmt.Errorf("missing aggregate path segment commitment")
	}
	out := &EncodedPath{
		Height: stored.AggregateHeight,
		Root:   root,
		Key:    append([]byte(nil), key...),
		Layout: PathLayout{
			NodeCount:    first.NodeCount,
			SourceCount:  first.SourceCount,
			FileSize:     SourceSymbolSize,
			CompactBytes: first.CompactBytes,
		},
		Seed:           append([]byte(nil), first.Seed...),
		Matrix:         matrix,
		Blocks:         blocks,
		Q:              q,
		Aggregate:      aggregate,
		AggregateStart: uint64(start),
	}
	if err := validateEncodedPath(out); err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func (s *EthDBStore) HistoricalLeaf(root common.Hash, key []byte) (*HistoricalLeaf, bool, error) {
	blob, err := s.get(historicalKey(root, key))
	if err != nil || blob == nil {
		return nil, false, err
	}
	var stored storedHistoricalLeaf
	if len(blob) > 0 && blob[0] == compactHistoryVersion {
		stored, err = decodeCompactHistory(blob)
	} else {
		err = rlp.DecodeBytes(blob, &stored)
	}
	if err != nil {
		return nil, false, err
	}
	framed, err := frameCompactLeaf(stored.Leaf)
	if err != nil {
		return nil, false, err
	}
	block, err := fileipa.BytesToFrChunks(framed)
	if err != nil {
		return nil, false, err
	}
	var (
		aggregate      *EpochAggregate
		aggregateIndex = -1
		sourceCount    = stored.SourceCount
	)
	if stored.Aggregated {
		aggregate, err = s.epochAggregate(stored.AggregateHeight, stored.AggregateID)
		if err != nil {
			return nil, false, err
		}
		for i := range aggregate.Relations {
			relation := aggregate.Relations[i]
			if relation.Kind == AggregateRelationHistoricalLeaf && relation.Root == root &&
				bytes.Equal(relation.Key, key) {
				aggregateIndex = i
				sourceCount = relation.Length / (SourceSymbolSize / SourceChunkSize)
				stored.StartHeight = relation.StartHeight
				stored.TerminalHeight = relation.TerminalHeight
				stored.NodeCount = relation.NodeCount
				stored.CompactBytes = relation.CompactBytes
				break
			}
		}
		if aggregateIndex < 0 || sourceCount == 0 {
			return nil, false, fmt.Errorf("aggregate historical relation not found")
		}
	}
	if sourceCount == 0 {
		return nil, false, fmt.Errorf("empty historical source layout")
	}
	if stored.NodeCount == 0 {
		return nil, false, fmt.Errorf("empty historical path layout")
	}
	layout := PathLayout{
		NodeCount:    stored.NodeCount,
		SourceCount:  sourceCount,
		FileSize:     SourceSymbolSize,
		CompactBytes: stored.CompactBytes,
	}
	selectorLength := nextPowerOfTwo(int(sourceCount) * (SourceSymbolSize / SourceChunkSize))
	if stored.Aggregated {
		selectorLength = int(sourceCount) * (SourceSymbolSize / SourceChunkSize)
	}
	selector, err := historicalSelector(layout, len(block), selectorLength)
	if err != nil {
		return nil, false, err
	}
	out := &HistoricalLeaf{
		StartHeight:    stored.StartHeight,
		TerminalHeight: stored.TerminalHeight,
		TerminalRoot:   root,
		Key:            append([]byte(nil), key...),
		Layout:         layout,
		Leaf:           append([]byte(nil), stored.Leaf...),
		Selector:       selector,
		Block:          block,
	}
	if aggregateIndex >= 0 {
		out.AggregateIndex = uint64(aggregateIndex)
	}
	if stored.Aggregated {
		out.Aggregate = aggregate
		q, ok := aggregateSegmentQ(aggregate, aggregate.Relations[out.AggregateIndex].Offset)
		if !ok {
			return nil, false, fmt.Errorf("missing aggregate historical segment commitment")
		}
		out.Q = q
		if err := validateHistoricalLeaf(out); err != nil {
			return nil, false, err
		}
		return out, true, nil
	}
	q, err := mptproofmsg.G1FromWire(stored.Q)
	if err != nil {
		return nil, false, err
	}
	out.Q = q
	params, err := ipa.NewTestParams(selectorLength)
	if err != nil {
		return nil, false, err
	}
	if len(stored.RowCs) < len(block) || len(stored.RowCs) != nextPowerOfTwo(len(stored.RowCs)) {
		return nil, false, fmt.Errorf("stored historical proof row count mismatch")
	}
	rows := make([]fileipa.FileIPARow, len(stored.RowCs))
	selectStart := uint64(int(stored.CompactBytes)/SourceChunkSize - len(block))
	for i := range stored.RowCs {
		c, err := mptproofmsg.ScalarFromWire(stored.RowCs[i])
		if err != nil {
			return nil, false, err
		}
		a := make([]fr.Element, selectorLength)
		if i < len(block) {
			a, err = historicalChunkSelector(selectStart, i, selectorLength)
			if err != nil {
				return nil, false, err
			}
		} else if !c.IsZero() {
			return nil, false, fmt.Errorf("stored historical padding row %d has non-zero C", i)
		}
		rows[i] = fileipa.FileIPARow{A: a, C: c}
	}
	proof, err := mptproofmsg.IPAProofFromWire(stored.IPAProof)
	if err != nil {
		return nil, false, err
	}
	challenges, err := loadOrDeriveFoldChallenges(stored.FoldChallenges, rows, q, historicalLeafDomain)
	if err != nil {
		return nil, false, err
	}
	out.Proof = &fileipa.FoldedFileIPAResult{
		Params:    params,
		Rows:      rows,
		FoldProof: &fileipa.FileFoldProof{Challenges: challenges},
		IPAProof:  proof,
	}
	if err := validateHistoricalLeaf(out); err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func (s *EthDBStore) epochAggregate(height uint64, id common.Hash) (*EpochAggregate, error) {
	s.aggregateMu.Lock()
	defer s.aggregateMu.Unlock()
	cacheKey := aggregateCacheKey{height: height, id: id}
	if aggregate := s.aggregateCache[cacheKey]; aggregate != nil {
		return aggregate, nil
	}
	blob, err := s.get(aggregateKey(height, id))
	if err != nil {
		return nil, err
	}
	if blob == nil {
		return nil, fmt.Errorf("missing epoch aggregate at height %d", height)
	}
	stored, err := decodeCompactAggregate(blob)
	if err != nil {
		return nil, err
	}
	if stored.Height != height || len(stored.Relations) == 0 {
		return nil, fmt.Errorf("invalid epoch aggregate record at height %d", height)
	}
	if err := normalizeStoredAggregate(&stored); err != nil {
		return nil, err
	}
	maxEnd := uint64(0)
	for _, relation := range stored.Relations {
		if relation.Length == 0 || relation.Offset > ^uint64(0)-relation.Length {
			return nil, fmt.Errorf("invalid aggregate relation dimensions")
		}
		if end := relation.Offset + relation.Length; end > maxEnd {
			maxEnd = end
		}
	}
	vectorLength := nextPowerOfTwo(int(maxEnd))
	params, err := ipa.NewTestParams(vectorLength)
	if err != nil {
		return nil, err
	}
	pathRelations := 0
	relationCount := 0
	for i := range stored.Relations {
		switch stored.Relations[i].Kind {
		case AggregateRelationPath:
			pathRelations += SourceSymbolSize / SourceChunkSize
			relationCount += SourceSymbolSize / SourceChunkSize
		case AggregateRelationHistoricalLeaf:
			if stored.Relations[i].SelectCount == 0 {
				return nil, fmt.Errorf("empty historical aggregate relation")
			}
			relationCount += int(stored.Relations[i].SelectCount)
		default:
			return nil, fmt.Errorf("unknown aggregate relation kind %d", stored.Relations[i].Kind)
		}
	}
	if len(stored.RowCs) != pathRelations {
		return nil, fmt.Errorf("invalid aggregate row count")
	}
	rows := make([]fileipa.FileIPARow, nextPowerOfTwo(relationCount))
	relations := make([]AggregateRelation, 0, relationCount)
	segments := make([]AggregateSegmentCommitment, len(stored.Segments))
	for i := range stored.Segments {
		q, err := mptproofmsg.G1FromWire(stored.Segments[i].Q)
		if err != nil {
			return nil, err
		}
		segments[i] = AggregateSegmentCommitment{Offset: stored.Segments[i].Offset, Q: q}
	}
	nextPathC := 0
	for groupIndex, relation := range stored.Relations {
		chunkCount := SourceSymbolSize / SourceChunkSize
		if relation.Kind == AggregateRelationHistoricalLeaf {
			chunkCount = int(relation.SelectCount)
		}
		for chunk := 0; chunk < chunkCount; chunk++ {
			localA, err := storedAggregateRelationA(relation, chunk)
			if err != nil {
				return nil, fmt.Errorf("load aggregate relation group %d chunk %d: %w", groupIndex, chunk, err)
			}
			if uint64(len(localA)) != relation.Length ||
				relation.Offset+relation.Length > uint64(vectorLength) {
				return nil, fmt.Errorf("aggregate relation group %d layout mismatch", groupIndex)
			}
			a := make([]fr.Element, vectorLength)
			copy(a[relation.Offset:], localA)
			var c fr.Element
			switch relation.Kind {
			case AggregateRelationPath:
				c, err = mptproofmsg.ScalarFromWire(stored.RowCs[nextPathC])
				if err != nil {
					return nil, err
				}
				nextPathC++
			case AggregateRelationHistoricalLeaf:
				c, err = s.historicalRelationC(relation, chunk)
				if err != nil {
					return nil, fmt.Errorf("load aggregate historical relation group %d: %w", groupIndex, err)
				}
			}
			rowIndex := len(relations)
			rows[rowIndex] = fileipa.FileIPARow{A: a, C: c}
			relations = append(relations, AggregateRelation{
				Kind:           relation.Kind,
				Root:           relation.Root,
				Key:            append([]byte(nil), relation.Key...),
				NodeCount:      relation.NodeCount,
				CompactBytes:   relation.CompactBytes,
				StartHeight:    relation.StartHeight,
				TerminalHeight: relation.TerminalHeight,
				Row:            relation.Row,
				Offset:         relation.Offset,
				Length:         relation.Length,
				C:              c,
				Seed:           append([]byte(nil), relation.Seed...),
				SourceCount:    relation.SourceCount,
				RowCount:       relation.RowCount,
				SelectStart:    relation.SelectStart,
				SelectCount:    relation.SelectCount,
			})
		}
	}
	for i := len(relations); i < len(rows); i++ {
		rows[i] = fileipa.FileIPARow{A: make([]fr.Element, vectorLength)}
	}
	challenges, err := loadOrDeriveFoldChallenges(
		stored.FoldChallenges, rows, sumG1(segmentPoints(segments)), epochAggregateDomain,
	)
	if err != nil {
		return nil, err
	}
	proof, err := mptproofmsg.IPAProofFromWire(stored.IPAProof)
	if err != nil {
		return nil, err
	}
	aggregate := &EpochAggregate{
		Height:       stored.Height,
		PathSeedBase: append([]byte(nil), stored.PathSeedBase...),
		Relations:    relations,
		Segments:     segments,
		Proof: &fileipa.FoldedFileIPAResult{
			Params:    params,
			Rows:      rows,
			FoldProof: &fileipa.FileFoldProof{Challenges: challenges},
			IPAProof:  proof,
		},
	}
	if epochAggregateID(aggregate) != id {
		return nil, fmt.Errorf("epoch aggregate content ID mismatch")
	}
	s.aggregateCache[cacheKey] = aggregate
	return aggregate, nil
}

func normalizeStoredAggregate(stored *storedEpochAggregate) error {
	if stored == nil {
		return fmt.Errorf("nil stored aggregate")
	}
	var (
		offset       uint64
		segmentIndex int
	)
	for i := 0; i < len(stored.Relations); {
		relation := &stored.Relations[i]
		groupEnd := i + 1
		if relation.Kind == AggregateRelationPath {
			for groupEnd < len(stored.Relations) {
				next := &stored.Relations[groupEnd]
				if next.Kind != AggregateRelationPath || !bytes.Equal(next.Key, relation.Key) {
					break
				}
				groupEnd++
			}
			rowCount := groupEnd - i
			for row := 0; row < rowCount; row++ {
				current := &stored.Relations[i+row]
				if current.Length == 0 ||
					current.Length%(SourceSymbolSize/SourceChunkSize) != 0 {
					return fmt.Errorf("invalid compact aggregate path length")
				}
				if current.Root == (common.Hash{}) {
					current.Root = stored.PathRoot
				}
				if current.Root == (common.Hash{}) {
					return fmt.Errorf("missing compact aggregate path root")
				}
				if len(current.Seed) == 0 {
					if len(stored.PathSeedBase) == 0 {
						return fmt.Errorf("missing compact aggregate path seed")
					}
					current.Seed = derivePathSeed(
						stored.PathSeedBase, stored.Height, current.Root, current.Key,
					)
				}
				current.SourceCount = current.Length / (SourceSymbolSize / SourceChunkSize)
				current.Row = uint64(row)
				current.RowCount = uint64(rowCount)
				current.Offset = offset
			}
		} else if relation.Kind == AggregateRelationHistoricalLeaf {
			relation.Offset = offset
		} else {
			return fmt.Errorf("unknown aggregate relation kind %d", relation.Kind)
		}
		if segmentIndex >= len(stored.Segments) {
			return fmt.Errorf("missing compact aggregate segment")
		}
		stored.Segments[segmentIndex].Offset = offset
		if relation.Length == 0 || offset > ^uint64(0)-relation.Length {
			return fmt.Errorf("invalid compact aggregate segment length")
		}
		offset += relation.Length
		segmentIndex++
		i = groupEnd
	}
	if segmentIndex != len(stored.Segments) {
		return fmt.Errorf("unused compact aggregate segments")
	}
	return nil
}

func (s *EthDBStore) historicalRelationC(relation storedAggregateRelation, chunk int) (fr.Element, error) {
	blob, err := s.get(historicalKey(relation.Root, relation.Key))
	if err != nil {
		return fr.Element{}, err
	}
	if blob == nil {
		return fr.Element{}, fmt.Errorf("missing historical leaf")
	}
	var stored storedHistoricalLeaf
	if len(blob) > 0 && blob[0] == compactHistoryVersion {
		stored, err = decodeCompactHistory(blob)
	} else {
		err = rlp.DecodeBytes(blob, &stored)
	}
	if err != nil {
		return fr.Element{}, err
	}
	framed, err := frameCompactLeaf(stored.Leaf)
	if err != nil {
		return fr.Element{}, err
	}
	block, err := fileipa.BytesToFrChunks(framed)
	if err != nil {
		return fr.Element{}, err
	}
	if chunk < 0 || chunk >= len(block) || uint64(len(block)) != relation.SelectCount {
		return fr.Element{}, fmt.Errorf("historical chunk %d out of range", chunk)
	}
	return block[chunk], nil
}

func storedAggregateRelationA(relation storedAggregateRelation, chunk int) ([]fr.Element, error) {
	switch relation.Kind {
	case AggregateRelationPath:
		matrix, err := GenerateMatrix(relation.Seed, int(relation.SourceCount), int(relation.RowCount))
		if err != nil {
			return nil, err
		}
		if relation.Row >= uint64(len(matrix)) {
			return nil, fmt.Errorf("path row %d out of range", relation.Row)
		}
		return expandCoefficientChunkRow(
			matrix[relation.Row],
			SourceSymbolSize/SourceChunkSize,
			chunk,
			int(relation.Length),
		)

	case AggregateRelationHistoricalLeaf:
		if relation.SelectCount == 0 || relation.SelectStart > relation.Length ||
			relation.SelectCount > relation.Length-relation.SelectStart {
			return nil, fmt.Errorf("invalid historical selector range")
		}
		if chunk < 0 || uint64(chunk) >= relation.SelectCount {
			return nil, fmt.Errorf("historical chunk %d out of range", chunk)
		}
		return historicalChunkSelector(relation.SelectStart, chunk, int(relation.Length))
	default:
		return nil, fmt.Errorf("unknown aggregate relation kind %d", relation.Kind)
	}
}

func aggregateSegmentQ(aggregate *EpochAggregate, offset uint64) (bn254.G1Affine, bool) {
	if aggregate == nil {
		return bn254.G1Affine{}, false
	}
	for _, segment := range aggregate.Segments {
		if segment.Offset == offset {
			return segment.Q, true
		}
	}
	return bn254.G1Affine{}, false
}

func aggregateRecordIdentity(
	aggregate *EpochAggregate,
	kind uint8,
	recordKey []byte,
) (common.Hash, []byte, error) {
	if aggregate == nil {
		return common.Hash{}, nil, fmt.Errorf("nil aggregate record")
	}
	for i := range aggregate.Relations {
		relation := aggregate.Relations[i]
		if relation.Kind != kind {
			continue
		}
		var candidate []byte
		if kind == AggregateRelationPath {
			candidate = pathKey(relation.Root, relation.Key)
		} else {
			candidate = historicalKey(relation.Root, relation.Key)
		}
		if bytes.Equal(candidate, recordKey) {
			return relation.Root, append([]byte(nil), relation.Key...), nil
		}
	}
	return common.Hash{}, nil, fmt.Errorf("aggregate record identity not found")
}

func compactAggregateRelations(relations []AggregateRelation) ([]storedAggregateRelation, error) {
	stored := make([]storedAggregateRelation, 0, len(relations))
	for i := 0; i < len(relations); {
		relation := relations[i]
		chunkCount := SourceSymbolSize / SourceChunkSize
		if relation.Kind == AggregateRelationHistoricalLeaf {
			chunkCount = int(relation.SelectCount)
		}
		if chunkCount <= 0 || i+chunkCount > len(relations) {
			return nil, fmt.Errorf("aggregate relation group at %d is incomplete", i)
		}
		for chunk := 1; chunk < chunkCount; chunk++ {
			other := relations[i+chunk]
			if other.Kind != relation.Kind || other.Root != relation.Root ||
				!bytes.Equal(other.Key, relation.Key) || other.Row != relation.Row ||
				other.NodeCount != relation.NodeCount || other.CompactBytes != relation.CompactBytes ||
				other.StartHeight != relation.StartHeight || other.TerminalHeight != relation.TerminalHeight ||
				other.Offset != relation.Offset || other.Length != relation.Length ||
				other.SourceCount != relation.SourceCount || other.RowCount != relation.RowCount ||
				other.SelectStart != relation.SelectStart || other.SelectCount != relation.SelectCount ||
				!bytes.Equal(other.Seed, relation.Seed) {
				return nil, fmt.Errorf("aggregate relation group at %d is not canonical", i)
			}
		}
		stored = append(stored, storedAggregateRelation{
			Kind:           relation.Kind,
			Root:           relation.Root,
			Key:            append([]byte(nil), relation.Key...),
			NodeCount:      relation.NodeCount,
			CompactBytes:   relation.CompactBytes,
			StartHeight:    relation.StartHeight,
			TerminalHeight: relation.TerminalHeight,
			Row:            relation.Row,
			Offset:         relation.Offset,
			Length:         relation.Length,
			Seed:           append([]byte(nil), relation.Seed...),
			SourceCount:    relation.SourceCount,
			RowCount:       relation.RowCount,
			SelectStart:    relation.SelectStart,
			SelectCount:    relation.SelectCount,
		})
		i += chunkCount
	}
	return stored, nil
}

func aggregatePathRoot(relations []AggregateRelation) (common.Hash, bool) {
	var (
		root common.Hash
		seen bool
	)
	for i := range relations {
		if relations[i].Kind != AggregateRelationPath {
			continue
		}
		if !seen {
			root, seen = relations[i].Root, true
		} else if relations[i].Root != root {
			return common.Hash{}, false
		}
	}
	return root, seen
}

func segmentPoints(segments []AggregateSegmentCommitment) []bn254.G1Affine {
	points := make([]bn254.G1Affine, len(segments))
	for i := range segments {
		points[i] = segments[i].Q
	}
	return points
}

func loadOrDeriveFoldChallenges(
	wire [][]mptproofmsg.ScalarWire,
	rows []fileipa.FileIPARow,
	q bn254.G1Affine,
	domain string,
) ([][]fr.Element, error) {
	if len(wire) != 0 {
		return mptproofmsg.ScalarMatrixFromWire(wire)
	}
	statements := make([]folding.Statement, len(rows))
	for i := range rows {
		statements[i] = folding.Statement{
			A: append([]fr.Element(nil), rows[i].A...),
			Q: q,
			C: rows[i].C,
		}
	}
	_, proof, err := folding.FoldStatements(statements, domain)
	if err != nil {
		return nil, err
	}
	return proof.Challenges, nil
}

// Audit loads and cryptographically verifies every persisted proof record.
func (s *EthDBStore) Audit() (*AuditSummary, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("nil fountain MPT ethdb store")
	}
	summary := new(AuditSummary)
	aggregateVerified := make(map[aggregateCacheKey]bool)
	if err := s.scanPrefix(rootPrefix, func(_, _ []byte) error {
		summary.Roots++
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.scanPrefix(versionPrefix, func(_, _ []byte) error {
		summary.Versions++
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.scanPrefix(pathPrefix, func(recordKey, value []byte) error {
		var stored storedEncodedPath
		var err error
		if len(value) > 0 && value[0] == compactPathVersion {
			stored, err = decodeCompactPath(value)
		} else {
			err = rlp.DecodeBytes(value, &stored)
		}
		if err != nil {
			return err
		}
		root, rawKey := stored.Root, stored.Key
		if stored.Aggregated {
			aggregate, err := s.epochAggregate(stored.AggregateHeight, stored.AggregateID)
			if err != nil {
				return err
			}
			root, rawKey, err = aggregateRecordIdentity(aggregate, AggregateRelationPath, recordKey)
			if err != nil {
				return err
			}
		}
		path, ok, err := s.EncodedPath(root, rawKey)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("encoded path disappeared during audit")
		}
		var verified bool
		if path.Aggregate != nil {
			if err := validateEncodedPath(path); err != nil {
				return err
			}
			var known bool
			cacheKey := aggregateCacheKey{height: path.Aggregate.Height, id: epochAggregateID(path.Aggregate)}
			verified, known = aggregateVerified[cacheKey]
			if !known {
				verified, err = VerifyEpochAggregate(path.Aggregate)
				if err != nil {
					return err
				}
				aggregateVerified[cacheKey] = verified
			}
		} else {
			verified, err = VerifyEncodedPath(path)
			if err != nil {
				return err
			}
		}
		summary.Paths = append(summary.Paths, AuditPath{
			Height:   path.Height,
			Root:     root,
			Key:      append([]byte(nil), rawKey...),
			Rows:     len(path.Matrix),
			Verified: verified,
		})
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.scanPrefix(historicalPrefix, func(recordKey, value []byte) error {
		var stored storedHistoricalLeaf
		var err error
		if len(value) > 0 && value[0] == compactHistoryVersion {
			stored, err = decodeCompactHistory(value)
		} else {
			err = rlp.DecodeBytes(value, &stored)
		}
		if err != nil {
			return err
		}
		root, rawKey := stored.TerminalRoot, stored.Key
		if stored.Aggregated {
			aggregate, err := s.epochAggregate(stored.AggregateHeight, stored.AggregateID)
			if err != nil {
				return err
			}
			root, rawKey, err = aggregateRecordIdentity(
				aggregate, AggregateRelationHistoricalLeaf, recordKey,
			)
			if err != nil {
				return err
			}
		}
		leaf, ok, err := s.HistoricalLeaf(root, rawKey)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("historical leaf disappeared during audit")
		}
		var verified bool
		if leaf.Aggregate != nil {
			if err := validateHistoricalLeaf(leaf); err != nil {
				return err
			}
			var known bool
			cacheKey := aggregateCacheKey{height: leaf.Aggregate.Height, id: epochAggregateID(leaf.Aggregate)}
			verified, known = aggregateVerified[cacheKey]
			if !known {
				verified, err = VerifyEpochAggregate(leaf.Aggregate)
				if err != nil {
					return err
				}
				aggregateVerified[cacheKey] = verified
			}
		} else {
			verified, err = VerifyHistoricalLeaf(leaf)
			if err != nil {
				return err
			}
		}
		summary.HistoricalLeaves = append(summary.HistoricalLeaves, AuditHistoricalLeaf{
			StartHeight:    leaf.StartHeight,
			TerminalHeight: leaf.TerminalHeight,
			TerminalRoot:   root,
			Key:            append([]byte(nil), rawKey...),
			Verified:       verified,
		})
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.scanPrefix(aggregatePrefix, func(_, _ []byte) error {
		summary.AggregateProofs++
		return nil
	}); err != nil {
		return nil, err
	}
	return summary, nil
}

// StorageBreakdown returns exact logical payload sizes for the records
// currently retained by the sidecar.
func (s *EthDBStore) StorageBreakdown() (*StorageBreakdown, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("nil fountain MPT ethdb store")
	}
	out := new(StorageBreakdown)
	if err := s.scanPrefix(nodeStatsPrefix, func(key, value []byte) error {
		var stats NodeUpdateStats
		if err := rlp.DecodeBytes(value, &stats); err != nil {
			return err
		}
		out.Baseline.Nodes += stats.Nodes
		out.Baseline.BlobBytes += stats.BlobBytes
		out.Baseline.HashBytes += stats.HashBytes
		out.Baseline.Deletes += stats.Deletes
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.scanPrefix(rootPrefix, func(key, value []byte) error {
		size := uint64(len(key) + len(value))
		out.RootRecordBytes += size
		out.RecordBytes += size
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.scanPrefix(versionPrefix, func(key, value []byte) error {
		size := uint64(len(key) + len(value))
		out.VersionIndexBytes += size
		out.RecordBytes += size
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.scanPrefix(pathPrefix, func(key, value []byte) error {
		var stored storedEncodedPath
		var err error
		if len(value) > 0 && value[0] == compactPathVersion {
			stored, err = decodeCompactPath(value)
		} else {
			err = rlp.DecodeBytes(value, &stored)
		}
		if err != nil {
			return err
		}
		recordBytes := uint64(len(key) + len(value))
		blocks := scalarMatrixWireBytes(stored.Blocks)
		matrix := uint64(0)
		commitment := uint64(len(stored.Q))
		statements := scalarWireBytes(stored.RowCs)
		foldChallenges := scalarMatrixWireBytes(stored.FoldChallenges)
		ipaProof := ipaProofWireBytes(stored.IPAProof)
		proof := statements + foldChallenges + ipaProof
		out.EncodedBlocksBytes += blocks
		out.GenerationMatrixBytes += matrix
		out.CommitmentBytes += commitment
		out.ProofBytes += proof
		out.ProofStatementBytes += statements
		out.ProofFoldChallengeBytes += foldChallenges
		out.ProofIPABytes += ipaProof
		out.OtherRecordMetadataBytes += recordBytes - blocks - matrix - commitment - proof
		out.RecordBytes += recordBytes
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.scanPrefix(historicalPrefix, func(key, value []byte) error {
		var stored storedHistoricalLeaf
		var err error
		if len(value) > 0 && value[0] == compactHistoryVersion {
			stored, err = decodeCompactHistory(value)
		} else {
			err = rlp.DecodeBytes(value, &stored)
		}
		if err != nil {
			return err
		}
		recordBytes := uint64(len(key) + len(value))
		leaf := uint64(len(stored.Leaf))
		matrix := uint64(0)
		commitment := uint64(len(stored.Q))
		statements := scalarWireBytes(stored.RowCs)
		foldChallenges := scalarMatrixWireBytes(stored.FoldChallenges)
		ipaProof := ipaProofWireBytes(stored.IPAProof)
		proof := statements + foldChallenges + ipaProof
		out.HistoricalLeafBytes += leaf
		out.GenerationMatrixBytes += matrix
		out.CommitmentBytes += commitment
		out.ProofBytes += proof
		out.ProofStatementBytes += statements
		out.ProofFoldChallengeBytes += foldChallenges
		out.ProofIPABytes += ipaProof
		out.OtherRecordMetadataBytes += recordBytes - leaf - matrix - commitment - proof
		out.RecordBytes += recordBytes
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.scanPrefix(aggregatePrefix, func(key, value []byte) error {
		stored, err := decodeCompactAggregate(value)
		if err != nil {
			return err
		}
		recordBytes := uint64(len(key) + len(value))
		var commitments uint64
		for _, segment := range stored.Segments {
			commitments += uint64(len(segment.Q))
		}
		blocks := scalarWireBytes(stored.RowCs)
		foldChallenges := scalarMatrixWireBytes(stored.FoldChallenges)
		ipaProof := ipaProofWireBytes(stored.IPAProof)
		proof := foldChallenges + ipaProof
		out.EncodedBlocksBytes += blocks
		out.CommitmentBytes += commitments
		out.ProofBytes += proof
		out.ProofFoldChallengeBytes += foldChallenges
		out.ProofIPABytes += ipaProof
		out.OtherRecordMetadataBytes += recordBytes - blocks - commitments - proof
		out.RecordBytes += recordBytes
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *EthDBStore) scanPrefix(prefix []byte, visit func(key, value []byte) error) error {
	iter := s.db.NewIterator(prefix, nil)
	defer iter.Release()
	for iter.Next() {
		key := append([]byte(nil), iter.Key()...)
		value := append([]byte(nil), iter.Value()...)
		if err := visit(key, value); err != nil {
			return err
		}
	}
	return iter.Error()
}

func (s *EthDBStore) get(key []byte) ([]byte, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("nil fountain MPT ethdb store")
	}
	has, err := s.db.Has(key)
	if err != nil || !has {
		return nil, err
	}
	return s.db.Get(key)
}

func rootKey(height uint64) []byte {
	key := append([]byte(nil), rootPrefix...)
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], height)
	return append(key, encoded[:]...)
}

func versionKey(root common.Hash, key []byte, start uint64) []byte {
	out := append([]byte(nil), versionPrefix...)
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], start)
	return append(out, crypto.Keccak256(root[:], key, encoded[:])...)
}

func pathKey(root common.Hash, key []byte) []byte {
	out := append([]byte(nil), pathPrefix...)
	return append(out, crypto.Keccak256(root[:], key)...)
}

func historicalKey(root common.Hash, key []byte) []byte {
	out := append([]byte(nil), historicalPrefix...)
	return append(out, crypto.Keccak256(root[:], key)...)
}

func nodeStatsKey(height uint64) []byte {
	key := append([]byte(nil), nodeStatsPrefix...)
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], height)
	return append(key, encoded[:]...)
}

func aggregateKey(height uint64, id common.Hash) []byte {
	key := append([]byte(nil), aggregatePrefix...)
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], height)
	key = append(key, encoded[:]...)
	return append(key, id[:]...)
}

func epochAggregateID(aggregate *EpochAggregate) common.Hash {
	if aggregate == nil {
		return common.Hash{}
	}
	var encoded [8]byte
	blob := make([]byte, 0, len(aggregate.Relations)*160)
	binary.BigEndian.PutUint64(encoded[:], aggregate.Height)
	blob = append(blob, encoded[:]...)
	for i := range aggregate.Relations {
		relation := aggregate.Relations[i]
		blob = append(blob, relation.Kind)
		blob = append(blob, relation.Root[:]...)
		binary.BigEndian.PutUint64(encoded[:], uint64(len(relation.Key)))
		blob = append(blob, encoded[:]...)
		blob = append(blob, relation.Key...)
		for _, value := range []uint64{
			relation.NodeCount, relation.CompactBytes, relation.StartHeight,
			relation.TerminalHeight,
			relation.Row, relation.Offset, relation.Length, relation.SourceCount,
			relation.RowCount, relation.SelectStart, relation.SelectCount,
		} {
			binary.BigEndian.PutUint64(encoded[:], value)
			blob = append(blob, encoded[:]...)
		}
		binary.BigEndian.PutUint64(encoded[:], uint64(len(relation.Seed)))
		blob = append(blob, encoded[:]...)
		blob = append(blob, relation.Seed...)
		c := relation.C.Bytes()
		blob = append(blob, c[:]...)
	}
	for i := range aggregate.Segments {
		binary.BigEndian.PutUint64(encoded[:], aggregate.Segments[i].Offset)
		blob = append(blob, encoded[:]...)
		blob = append(blob, mptproofmsg.G1ToWire(aggregate.Segments[i].Q)...)
	}
	return crypto.Keccak256Hash(blob)
}

func expandCoefficientChunkRow(row []fr.Element, chunksPerFile, chunk, vectorLength int) ([]fr.Element, error) {
	if chunksPerFile <= 0 {
		return nil, fmt.Errorf("invalid chunks per file %d", chunksPerFile)
	}
	if chunk < 0 || chunk >= chunksPerFile {
		return nil, fmt.Errorf("chunk %d outside chunks per file %d", chunk, chunksPerFile)
	}
	semanticLength := len(row) * chunksPerFile
	if vectorLength < semanticLength {
		return nil, fmt.Errorf("vector length %d below semantic length %d", vectorLength, semanticLength)
	}
	expanded := make([]fr.Element, vectorLength)
	for source := range row {
		expanded[source*chunksPerFile+chunk].Set(&row[source])
	}
	return expanded, nil
}

func scalarWireBytes(row []mptproofmsg.ScalarWire) uint64 {
	var size uint64
	for _, scalar := range row {
		size += uint64(len(scalar))
	}
	return size
}

func scalarMatrixWireBytes(matrix [][]mptproofmsg.ScalarWire) uint64 {
	var size uint64
	for _, row := range matrix {
		size += scalarWireBytes(row)
	}
	return size
}

func fileIPARowWireBytes(row mptproofmsg.FileIPARowWire) uint64 {
	return scalarWireBytes(row.A) + uint64(len(row.C))
}

func fileIPARowsWireBytes(rows []mptproofmsg.FileIPARowWire) uint64 {
	var size uint64
	for _, row := range rows {
		size += fileIPARowWireBytes(row)
	}
	return size
}

func ipaProofWireBytes(proof mptproofmsg.IPAProofWire) uint64 {
	size := uint64(len(proof.AFinal) + len(proof.BFinal))
	for _, point := range proof.L {
		size += uint64(len(point))
	}
	for _, point := range proof.R {
		size += uint64(len(point))
	}
	return size
}

func scalarBlocksToWire(blocks [][]fr.Element) [][]mptproofmsg.ScalarWire {
	out := make([][]mptproofmsg.ScalarWire, len(blocks))
	for i := range blocks {
		out[i] = mptproofmsg.ScalarsToWire(blocks[i])
	}
	return out
}

func scalarBlocksFromWire(blocks [][]mptproofmsg.ScalarWire) ([][]fr.Element, error) {
	out := make([][]fr.Element, len(blocks))
	for i := range blocks {
		row, err := mptproofmsg.ScalarsFromWire(blocks[i])
		if err != nil {
			return nil, fmt.Errorf("block %d: %w", i, err)
		}
		out[i] = row
	}
	return out, nil
}
