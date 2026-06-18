package partitionedmptshadow

import (
	"encoding/binary"
	"fmt"
	"sort"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/internal/mptagg"
	"github.com/ethereum/go-ethereum/rlp"
)

var (
	shadowResultPrefix    = []byte("partition-shadow/result/")
	shadowRawSetPrefix    = []byte("partition-shadow/rawset/")
	shadowLocalViewPrefix = []byte("partition-shadow/localview/")
)

type StoredResult struct {
	Root             common.Hash
	Parent           common.Hash
	Block            uint64
	PartitionCount   uint64
	ChangedPathKeys  [][]byte
	PartitionIDs     []uint64
	RawSetKeys       [][]byte
	LocalViewKeys    [][]byte
	PreviousRootHash string
	DirtyNodeStats   []DirtyNodeStats `rlp:"tail"`
}

type StoredRawSet struct {
	Root        common.Hash
	PartitionID uint64
	Hashes      []string
	Nodes       []StoredRawNodeMeta `rlp:"tail"`
}

type StoredRawNodeMeta struct {
	Hash     []byte
	BlobSize uint64
}

type StoredLocalView struct {
	Root               common.Hash
	PartitionID        uint64
	RawPathKeys        [][]byte
	PreservedPathKeys  [][]byte
	RawAvailableHashes []string
	AggregatedNodes    []StoredAggregatedNode
}

type StoredAggregatedNode struct {
	ID                    string
	PathKey               []byte
	Nibbles               []byte
	CommitmentPrefixZeros uint64
	Path                  []byte
	NodeCount             uint64
	OriginalNodeHashes    [][]byte
	Commitment            []byte
	ChunkCount            uint64
	ElementCount          uint64
	NextHash              []byte
	HasNextPosition       bool
	NextPosition          uint64
	CommitmentDigest      []byte
	AttachedRawNodes      []StoredAttachedRawNode
}

type StoredAttachedRawNode struct {
	Hash     []byte
	Position uint64
	PathKey  []byte
	NodeID   string
	Reason   string
}

type EthDBStore struct {
	db         ethdb.Database
	mu         sync.RWMutex
	historical map[int]map[string]bool
}

func NewEthDBStore(db ethdb.Database) *EthDBStore {
	return &EthDBStore{db: db, historical: make(map[int]map[string]bool)}
}

func (s *EthDBStore) HistoricalRawNodeSet(partitionID int) map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneBoolMap(s.historical[partitionID])
}

func (s *EthDBStore) SaveRawNodeSet(root common.Hash, partitionID int, rawSet map[string]bool) error {
	return s.SaveRawNodeSetWithMetadata(root, partitionID, rawSet, nil)
}

func (s *EthDBStore) SaveRawNodeSetWithMetadata(root common.Hash, partitionID int, rawSet map[string]bool, nodeSizes map[string]int) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("nil shadow ethdb store")
	}
	stored := StoredRawSet{
		Root:        root,
		PartitionID: uint64(partitionID),
		Hashes:      sortedRawSetHashes(rawSet),
	}
	for _, hash := range stored.Hashes {
		size := nodeSizes[normalizeStoreRawHashKey(hash)]
		if size <= 0 {
			continue
		}
		stored.Nodes = append(stored.Nodes, StoredRawNodeMeta{
			Hash:     common.HexToHash(hash).Bytes(),
			BlobSize: uint64(size),
		})
	}
	blob, err := rlp.EncodeToBytes(stored)
	if err != nil {
		return err
	}
	if err := s.db.Put(rawSetKey(root, partitionID), blob); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.historical == nil {
		s.historical = make(map[int]map[string]bool)
	}
	if s.historical[partitionID] == nil {
		s.historical[partitionID] = make(map[string]bool)
	}
	for _, hash := range stored.Hashes {
		s.historical[partitionID][hash] = true
	}
	return nil
}

func (s *EthDBStore) RawNodeSet(root common.Hash, partitionID int) (map[string]bool, bool) {
	stored, ok, err := s.StoredRawSet(root, partitionID)
	if err != nil || !ok {
		return nil, false
	}
	out := make(map[string]bool, len(stored.Hashes))
	for _, hash := range stored.Hashes {
		out[hash] = true
	}
	return out, true
}

func (s *EthDBStore) StoredRawSet(root common.Hash, partitionID int) (*StoredRawSet, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, fmt.Errorf("nil shadow ethdb store")
	}
	key := rawSetKey(root, partitionID)
	has, err := s.db.Has(key)
	if err != nil || !has {
		return nil, false, err
	}
	blob, err := s.db.Get(key)
	if err != nil {
		return nil, false, err
	}
	var stored StoredRawSet
	if err := rlp.DecodeBytes(blob, &stored); err != nil {
		return nil, false, err
	}
	return &stored, true, nil
}

func (s *EthDBStore) SaveResult(result *Result) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("nil shadow ethdb store")
	}
	if result == nil {
		return fmt.Errorf("nil shadow result")
	}
	stored := StoredResult{
		Root:             result.Root,
		Parent:           result.Parent,
		Block:            result.Block,
		PreviousRootHash: "",
		DirtyNodeStats:   []DirtyNodeStats{result.DirtyNodeStats},
	}
	if result.PartitionResult != nil {
		stored.PartitionCount = uint64(len(result.PartitionResult.Partitions))
		stored.PreviousRootHash = result.PartitionResult.PreviousRootHash
		for _, path := range result.PartitionResult.Diff.Changed {
			stored.ChangedPathKeys = append(stored.ChangedPathKeys, append([]byte(nil), path.Key...))
		}
		for _, partition := range result.PartitionResult.Partitions {
			if len(result.LocalViews) > 0 {
				if _, ok := result.LocalViews[partition.ID]; !ok {
					continue
				}
			}
			stored.PartitionIDs = append(stored.PartitionIDs, uint64(partition.ID))
			stored.RawSetKeys = append(stored.RawSetKeys, rawSetKey(result.Root, partition.ID))
			stored.LocalViewKeys = append(stored.LocalViewKeys, localViewKey(result.Root, partition.ID))
		}
	}
	blob, err := rlp.EncodeToBytes(stored)
	if err != nil {
		return err
	}
	if err := s.db.Put(resultKey(result.Root), blob); err != nil {
		return err
	}
	for partitionID, view := range result.LocalViews {
		if err := s.SaveLocalView(result.Root, partitionID, view); err != nil {
			return err
		}
	}
	return nil
}

func (s *EthDBStore) Result(root common.Hash) (*Result, bool) {
	return nil, false
}

func (s *EthDBStore) StoredResult(root common.Hash) (*StoredResult, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, fmt.Errorf("nil shadow ethdb store")
	}
	key := resultKey(root)
	has, err := s.db.Has(key)
	if err != nil || !has {
		return nil, false, err
	}
	blob, err := s.db.Get(key)
	if err != nil {
		return nil, false, err
	}
	var stored StoredResult
	if err := rlp.DecodeBytes(blob, &stored); err != nil {
		return nil, false, err
	}
	return &stored, true, nil
}

func (s *EthDBStore) SaveLocalView(root common.Hash, partitionID int, view *mptagg.LocalPartitionView) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("nil shadow ethdb store")
	}
	if view == nil {
		return nil
	}
	stored := StoredLocalView{
		Root:              root,
		PartitionID:       uint64(partitionID),
		RawPathKeys:       cloneBytes2D(view.RawPathKeys),
		PreservedPathKeys: cloneBytes2D(view.PreservedLatestPathKeys),
	}
	stored.RawAvailableHashes = sortedRawSetHashes(view.RawAvailableHashes)
	for _, agg := range view.AggregatedNodes {
		stored.AggregatedNodes = append(stored.AggregatedNodes, storedAggregatedNode(agg))
	}
	blob, err := rlp.EncodeToBytes(stored)
	if err != nil {
		return err
	}
	return s.db.Put(localViewKey(root, partitionID), blob)
}

func (s *EthDBStore) StoredLocalView(root common.Hash, partitionID int) (*StoredLocalView, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, fmt.Errorf("nil shadow ethdb store")
	}
	key := localViewKey(root, partitionID)
	has, err := s.db.Has(key)
	if err != nil || !has {
		return nil, false, err
	}
	blob, err := s.db.Get(key)
	if err != nil {
		return nil, false, err
	}
	var stored StoredLocalView
	if err := rlp.DecodeBytes(blob, &stored); err != nil {
		return nil, false, err
	}
	return &stored, true, nil
}

func storedAggregatedNode(agg *mptagg.AggregatedNode) StoredAggregatedNode {
	if agg == nil {
		return StoredAggregatedNode{}
	}
	commitment := agg.Commitment.Bytes()
	stored := StoredAggregatedNode{
		ID:                    agg.ID,
		PathKey:               append([]byte(nil), agg.PathKey...),
		Nibbles:               append([]byte(nil), agg.Nibbles...),
		CommitmentPrefixZeros: uint64(agg.CommitmentPrefixZeros),
		Path:                  append([]byte(nil), agg.Path...),
		NodeCount:             uint64(agg.NodeCount),
		OriginalNodeHashes:    cloneBytes2D(agg.OriginalNodeHashes),
		Commitment:            append([]byte(nil), commitment[:]...),
		ChunkCount:            uint64(agg.ChunkCount),
		ElementCount:          uint64(agg.ElementCount),
		NextHash:              append([]byte(nil), agg.NextHash...),
		CommitmentDigest:      append([]byte(nil), agg.CommitmentDigest...),
	}
	if agg.NextPosition >= 0 {
		stored.HasNextPosition = true
		stored.NextPosition = uint64(agg.NextPosition)
	}
	for _, attached := range agg.AttachedRawNodes {
		stored.AttachedRawNodes = append(stored.AttachedRawNodes, StoredAttachedRawNode{
			Hash:     append([]byte(nil), attached.Hash...),
			Position: uint64(attached.Position),
			PathKey:  append([]byte(nil), attached.PathKey...),
			NodeID:   attached.NodeID,
			Reason:   attached.Reason,
		})
	}
	return stored
}

func resultKey(root common.Hash) []byte {
	return append(append([]byte(nil), shadowResultPrefix...), root.Bytes()...)
}

func rawSetKey(root common.Hash, partitionID int) []byte {
	key := append(append([]byte(nil), shadowRawSetPrefix...), root.Bytes()...)
	return appendUint64(key, uint64(partitionID))
}

func localViewKey(root common.Hash, partitionID int) []byte {
	key := append(append([]byte(nil), shadowLocalViewPrefix...), root.Bytes()...)
	return appendUint64(key, uint64(partitionID))
}

func appendUint64(key []byte, value uint64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], value)
	return append(key, buf[:]...)
}

func sortedRawSetHashes(rawSet map[string]bool) []string {
	hashes := make([]string, 0, len(rawSet))
	for hash, keep := range rawSet {
		if keep {
			hashes = append(hashes, normalizeStoreRawHashKey(hash))
		}
	}
	sort.Strings(hashes)
	return hashes
}

func normalizeStoreRawHashKey(hash string) string {
	if len(hash) >= 2 && hash[:2] == "0x" {
		return hash[2:]
	}
	return hash
}

func cloneBytes2D(in [][]byte) [][]byte {
	if in == nil {
		return nil
	}
	out := make([][]byte, len(in))
	for i := range in {
		out[i] = append([]byte(nil), in[i]...)
	}
	return out
}
