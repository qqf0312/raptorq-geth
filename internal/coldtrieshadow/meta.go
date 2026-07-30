package coldtrieshadow

import (
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

type stateMeta struct {
	nodeHash       common.Hash
	accessTime     uint64
	creationHeight uint64
	timer          uint64
}

type stateMetaIndex struct {
	mu    sync.RWMutex
	t     uint64
	f     uint64
	metas map[string]*stateMeta
}

func newStateMetaIndex(threshold uint64) *stateMetaIndex {
	if threshold == 0 {
		threshold = DefaultThreshold
	}
	return &stateMetaIndex{
		t:     threshold,
		f:     defaultAccessF,
		metas: make(map[string]*stateMeta),
	}
}

func (idx *stateMetaIndex) access(key []byte, height uint64, nodeHash common.Hash) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	mapKey := string(key)
	meta, ok := idx.metas[mapKey]
	if !ok {
		meta = &stateMeta{
			nodeHash:       nodeHash,
			creationHeight: height,
		}
		idx.metas[mapKey] = meta
	}
	meta.nodeHash = nodeHash
	meta.accessTime++

	timer := height + idx.t
	accessTimer := meta.creationHeight + meta.accessTime/idx.f
	if accessTimer > timer {
		timer = accessTimer
	}
	meta.timer = timer
}

func (idx *stateMetaIndex) delete(key []byte) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.metas, string(key))
}

func (idx *stateMetaIndex) collectCold(height uint64) map[string]common.Hash {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	cold := make(map[string]common.Hash)
	for key, meta := range idx.metas {
		if height >= meta.timer {
			cold[key] = meta.nodeHash
		}
	}
	return cold
}
