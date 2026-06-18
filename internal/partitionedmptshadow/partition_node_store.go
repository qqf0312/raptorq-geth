package partitionedmptshadow

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/internal/mptagg"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

const partitionCompressedNodeMarker byte = 0x01

// PartitionNodeStore persists the partition-local trie surface into a sidecar DB.
// Raw MPT nodes are written using the same legacy hash->blob layout as chaindata;
// compressed nodes are written at boundaryHash||marker.
type PartitionNodeStore interface {
	SavePartitionNodes(root common.Hash, views map[int]*mptagg.LocalPartitionView, dirty *trienode.MergedNodeSet) error
}

type PartitionNodeDBStore struct {
	db ethdb.KeyValueStore
}

type StoredPartitionCompressedNode struct {
	Commitment     []byte
	CoveredHashes  [][]byte
	AttachedHashes [][]byte
}

func NewPartitionNodeDBStore(db ethdb.KeyValueStore) *PartitionNodeDBStore {
	return &PartitionNodeDBStore{db: db}
}

func (s *PartitionNodeDBStore) SavePartitionNodes(root common.Hash, views map[int]*mptagg.LocalPartitionView, dirty *trienode.MergedNodeSet) error {
	_ = root
	if s == nil || s.db == nil {
		return fmt.Errorf("nil partition node store")
	}
	if len(views) == 0 {
		return nil
	}
	for _, view := range views {
		if view == nil {
			continue
		}
		if err := s.saveRawNodes(view, dirty); err != nil {
			return err
		}
		if err := s.saveCompressedNodes(view); err != nil {
			return err
		}
	}
	return nil
}

func (s *PartitionNodeDBStore) saveRawNodes(view *mptagg.LocalPartitionView, dirty *trienode.MergedNodeSet) error {
	if dirty == nil {
		return nil
	}
	owned := view.RawAvailableHashes
	if len(owned) == 0 {
		return nil
	}
	for _, set := range dirty.Sets {
		if set == nil {
			continue
		}
		set.ForEachWithOrder(func(_ string, node *trienode.Node) {
			if node == nil || node.IsDeleted() || !ownedRawHash(owned, node.Hash) {
				return
			}
			rawdb.WriteLegacyTrieNode(s.db, node.Hash, node.Blob)
		})
	}
	return nil
}

func (s *PartitionNodeDBStore) saveCompressedNodes(view *mptagg.LocalPartitionView) error {
	for _, agg := range view.AggregatedNodes {
		if agg == nil || len(agg.OriginalNodeHashes) == 0 {
			continue
		}
		key, ok := compressedNodeKeyFromHash(agg.OriginalNodeHashes[0])
		if !ok {
			continue
		}
		commitment := agg.Commitment.Bytes()
		value := StoredPartitionCompressedNode{
			Commitment:    append([]byte(nil), commitment[:]...),
			CoveredHashes: cloneBytes2D(agg.OriginalNodeHashes[1:]),
		}
		for _, attached := range agg.AttachedRawNodes {
			if len(attached.Hash) > 0 {
				value.AttachedHashes = append(value.AttachedHashes, append([]byte(nil), attached.Hash...))
			}
		}
		blob, err := rlp.EncodeToBytes(value)
		if err != nil {
			return err
		}
		if err := s.db.Put(key, blob); err != nil {
			return err
		}
	}
	return nil
}

func ReadPartitionCompressedNode(db ethdb.KeyValueReader, boundary common.Hash) (*StoredPartitionCompressedNode, bool, error) {
	key := PartitionCompressedNodeKey(boundary)
	has, err := db.Has(key)
	if err != nil || !has {
		return nil, false, err
	}
	blob, err := db.Get(key)
	if err != nil {
		return nil, false, err
	}
	var node StoredPartitionCompressedNode
	if err := rlp.DecodeBytes(blob, &node); err != nil {
		return nil, false, err
	}
	return &node, true, nil
}

func PartitionCompressedNodeKey(boundary common.Hash) []byte {
	key := append([]byte(nil), boundary.Bytes()...)
	return append(key, partitionCompressedNodeMarker)
}

func compressedNodeKeyFromHash(hash []byte) ([]byte, bool) {
	if len(hash) != common.HashLength {
		return nil, false
	}
	return PartitionCompressedNodeKey(common.BytesToHash(hash)), true
}

func ownedRawHash(rawSet map[string]bool, hash common.Hash) bool {
	if len(rawSet) == 0 || hash == (common.Hash{}) {
		return false
	}
	hex := hash.Hex()
	return rawSet[hex] || rawSet[hex[2:]]
}
