package fountainmptshadow

import (
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

type MemoryStore struct {
	mu sync.RWMutex

	roots      map[uint64]common.Hash
	versions   []VersionRecord
	paths      map[string]*EncodedPath
	historical map[string]*HistoricalLeaf
	aggregates map[uint64]*EpochAggregate
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		roots:      make(map[uint64]common.Hash),
		paths:      make(map[string]*EncodedPath),
		historical: make(map[string]*HistoricalLeaf),
		aggregates: make(map[uint64]*EpochAggregate),
	}
}

func (s *MemoryStore) SaveEpochAggregate(aggregate *EpochAggregate) error {
	if s == nil || aggregate == nil {
		return fmt.Errorf("nil memory store or epoch aggregate")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aggregates[aggregate.Height] = aggregate
	return nil
}

func (s *MemoryStore) SaveRoots(roots []RootRecord) error {
	if s == nil {
		return fmt.Errorf("nil memory store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range roots {
		s.roots[record.Height] = record.Root
	}
	return nil
}

func (s *MemoryStore) SaveVersions(versions []VersionRecord) error {
	if s == nil {
		return fmt.Errorf("nil memory store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versions = cloneVersions(versions)
	return nil
}

func (s *MemoryStore) SaveEncodedPath(path *EncodedPath) error {
	if s == nil || path == nil {
		return fmt.Errorf("nil memory store or encoded path")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paths[pathStoreKey(path.Root, path.Key)] = path
	return nil
}

func (s *MemoryStore) SaveHistoricalLeaf(leaf *HistoricalLeaf) error {
	if s == nil || leaf == nil {
		return fmt.Errorf("nil memory store or historical leaf")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historical[historicalStoreKey(leaf.TerminalRoot, leaf.Key)] = leaf
	return nil
}

func (s *MemoryStore) Root(height uint64) (common.Hash, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	root, ok := s.roots[height]
	return root, ok
}

func (s *MemoryStore) Versions() []VersionRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneVersions(s.versions)
}

func (s *MemoryStore) EncodedPath(root common.Hash, key []byte) (*EncodedPath, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	path, ok := s.paths[pathStoreKey(root, key)]
	return path, ok
}

func (s *MemoryStore) HistoricalLeaf(root common.Hash, key []byte) (*HistoricalLeaf, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	leaf, ok := s.historical[historicalStoreKey(root, key)]
	return leaf, ok
}

func pathStoreKey(root common.Hash, key []byte) string {
	return string(root[:]) + "/" + string(key)
}

func historicalStoreKey(root common.Hash, key []byte) string {
	return pathStoreKey(root, key)
}
