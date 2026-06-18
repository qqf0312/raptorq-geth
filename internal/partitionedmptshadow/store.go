package partitionedmptshadow

import (
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

// Store persists shadow partition/local-view outputs. The first implementation
// is in-memory so the production hook can be designed separately.
type Store interface {
	HistoricalRawNodeSet(partitionID int) map[string]bool
	SaveRawNodeSet(root common.Hash, partitionID int, rawSet map[string]bool) error
	RawNodeSet(root common.Hash, partitionID int) (map[string]bool, bool)
	SaveResult(result *Result) error
	Result(root common.Hash) (*Result, bool)
}

type MemoryStore struct {
	mu         sync.RWMutex
	historical map[int]map[string]bool
	rawSets    map[common.Hash]map[int]map[string]bool
	results    map[common.Hash]*Result
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		historical: make(map[int]map[string]bool),
		rawSets:    make(map[common.Hash]map[int]map[string]bool),
		results:    make(map[common.Hash]*Result),
	}
}

func (s *MemoryStore) HistoricalRawNodeSet(partitionID int) map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneBoolMap(s.historical[partitionID])
}

func (s *MemoryStore) SaveRawNodeSet(root common.Hash, partitionID int, rawSet map[string]bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rawSets == nil {
		s.rawSets = make(map[common.Hash]map[int]map[string]bool)
	}
	if s.historical == nil {
		s.historical = make(map[int]map[string]bool)
	}
	if s.rawSets[root] == nil {
		s.rawSets[root] = make(map[int]map[string]bool)
	}
	s.rawSets[root][partitionID] = cloneBoolMap(rawSet)
	if s.historical[partitionID] == nil {
		s.historical[partitionID] = make(map[string]bool)
	}
	for hash, keep := range rawSet {
		if keep {
			s.historical[partitionID][hash] = true
		}
	}
	return nil
}

func (s *MemoryStore) RawNodeSet(root common.Hash, partitionID int) (map[string]bool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byPartition := s.rawSets[root]
	if byPartition == nil {
		return nil, false
	}
	rawSet, ok := byPartition[partitionID]
	return cloneBoolMap(rawSet), ok
}

func (s *MemoryStore) SaveResult(result *Result) error {
	if result == nil {
		return fmt.Errorf("nil shadow result")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.results == nil {
		s.results = make(map[common.Hash]*Result)
	}
	s.results[result.Root] = result
	return nil
}

func (s *MemoryStore) Result(root common.Hash) (*Result, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result, ok := s.results[root]
	return result, ok
}

func cloneBoolMap(in map[string]bool) map[string]bool {
	if in == nil {
		return nil
	}
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
