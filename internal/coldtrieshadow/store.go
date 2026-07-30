package coldtrieshadow

import (
	"encoding/binary"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/rlp"
)

var (
	statsPrefix = []byte("coldtrie-stats-")
	rootPrefix  = []byte("coldtrie-root-")
	nodePrefix  = []byte("coldtrie-node-")
	pathPrefix  = []byte("coldtrie-path-")
)

// Store persists cold trie snapshots and statistics.
type Store interface {
	Save(block uint64, stats StoredColdTrieStats, nodes []StoredColdTrieNode, paths []StoredColdPathCost) error
}

type EthDBStore struct {
	db ethdb.Database
}

func NewEthDBStore(db ethdb.Database) *EthDBStore {
	return &EthDBStore{db: db}
}

func (s *EthDBStore) Save(block uint64, stats StoredColdTrieStats, nodes []StoredColdTrieNode, paths []StoredColdPathCost) error {
	statsBlob, err := rlp.EncodeToBytes(stats)
	if err != nil {
		return err
	}
	rootBlob, err := rlp.EncodeToBytes([]common.Hash{stats.StateRoot, stats.ColdRoot})
	if err != nil {
		return err
	}
	batch := s.db.NewBatch()
	if err := batch.Put(statsKey(block), statsBlob); err != nil {
		return err
	}
	if err := batch.Put(rootKey(block), rootBlob); err != nil {
		return err
	}
	for _, node := range nodes {
		if err := batch.Put(nodeKey(stats.ColdRoot, node.Hash), node.Blob); err != nil {
			return err
		}
	}
	for _, path := range paths {
		blob, err := rlp.EncodeToBytes(path)
		if err != nil {
			return err
		}
		if err := batch.Put(pathKey(stats.ColdRoot, path.Key), blob); err != nil {
			return err
		}
	}
	return batch.Write()
}

func ReadStats(db ethdb.KeyValueReader, block uint64) (*StoredColdTrieStats, error) {
	blob, err := db.Get(statsKey(block))
	if err != nil {
		return nil, err
	}
	var stats StoredColdTrieStats
	if err := rlp.DecodeBytes(blob, &stats); err != nil {
		return nil, err
	}
	return &stats, nil
}

func ReadNode(db ethdb.KeyValueReader, coldRoot common.Hash, nodeHash common.Hash) []byte {
	blob, err := db.Get(nodeKey(coldRoot, nodeHash))
	if err != nil {
		return nil
	}
	return blob
}

func statsKey(block uint64) []byte {
	return append(statsPrefix, encodeBlock(block)...)
}

func rootKey(block uint64) []byte {
	return append(rootPrefix, encodeBlock(block)...)
}

func nodeKey(coldRoot common.Hash, nodeHash common.Hash) []byte {
	key := make([]byte, 0, len(nodePrefix)+common.HashLength+common.HashLength)
	key = append(key, nodePrefix...)
	key = append(key, coldRoot.Bytes()...)
	key = append(key, nodeHash.Bytes()...)
	return key
}

func pathKey(coldRoot common.Hash, path []byte) []byte {
	key := make([]byte, 0, len(pathPrefix)+common.HashLength+len(path))
	key = append(key, pathPrefix...)
	key = append(key, coldRoot.Bytes()...)
	key = append(key, path...)
	return key
}

func encodeBlock(block uint64) []byte {
	var enc [8]byte
	binary.BigEndian.PutUint64(enc[:], block)
	return common.CopyBytes(enc[:])
}
