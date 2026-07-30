package fountainmptshadow

import (
	"encoding/binary"
	"fmt"
	"sort"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/trie/triestate"
)

// UpdateDetector translates one trie database update into key-version events.
// It is kept outside trie.Database so deployments can choose account/storage
// key semantics without changing trie node code.
type UpdateDetector func(db *trie.Database, root common.Hash, parent common.Hash, block uint64, nodes *trienode.MergedNodeSet, states *triestate.Set) ([]KeyUpdate, error)

// Adapter is an external trie.Database update hook. It only records roots and
// version events; epoch finalization and pruning remain explicit operations.
type Adapter struct {
	Processor     *Processor
	DetectUpdates UpdateDetector
}

var _ trie.DatabaseUpdateHook = (*Adapter)(nil)

func (a *Adapter) OnTrieDatabaseUpdate(db *trie.Database, root common.Hash, parent common.Hash, block uint64, nodes *trienode.MergedNodeSet, states *triestate.Set) error {
	if a == nil || a.Processor == nil {
		return fmt.Errorf("nil fountain MPT shadow adapter")
	}
	var updates []KeyUpdate
	if a.DetectUpdates != nil {
		var err error
		updates, err = a.DetectUpdates(db, root, parent, block, nodes, states)
		if err != nil {
			return err
		}
	}
	return a.Processor.RecordHeightWithParent(block, root, parent, updates)
}

// TriePathResolver reads the original path nodes from a retained MPT root.
type TriePathResolver struct {
	DB *trie.Database
}

type EpochAdapterConfig struct {
	EpochLength uint64
	MinimumRows int
	Seed        []byte
	NodeCount   uint64
	NodeIndex   uint64
}

// EpochAdapter is the production-facing external hook. It rolls processors at
// epoch boundaries, detects account-key versions from triestate, encodes the
// locally owned final paths, and persists historical leaf proofs.
type EpochAdapter struct {
	mu sync.Mutex

	config EpochAdapterConfig
	store  Store

	processor  *Processor
	epochStart uint64
	epochEnd   uint64

	lastRecordedHeight uint64
	hasRecordedHeight  bool
}

func NewEpochAdapter(config EpochAdapterConfig, store Store) (*EpochAdapter, error) {
	if config.EpochLength == 0 {
		return nil, fmt.Errorf("zero epoch length")
	}
	if len(config.Seed) == 0 {
		return nil, fmt.Errorf("empty epoch adapter seed")
	}
	if config.NodeCount == 0 {
		config.NodeCount = 1
	}
	if config.NodeIndex >= config.NodeCount {
		return nil, fmt.Errorf("node index %d outside node count %d", config.NodeIndex, config.NodeCount)
	}
	if store == nil {
		return nil, fmt.Errorf("nil epoch adapter store")
	}
	return &EpochAdapter{config: config, store: store}, nil
}

var _ trie.DatabaseUpdateHook = (*EpochAdapter)(nil)

func (a *EpochAdapter) OnTrieDatabaseUpdate(db *trie.Database, root common.Hash, parent common.Hash, block uint64, nodes *trienode.MergedNodeSet, states *triestate.Set) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if recorder, ok := a.store.(interface {
		SaveNodeUpdateStats(NodeUpdateStats) error
	}); ok {
		if err := recorder.SaveNodeUpdateStats(measureNodeUpdates(block, nodes)); err != nil {
			return err
		}
	}
	updates, err := detectAccountUpdates(db, root, states, a.ownsKey)
	if err != nil {
		return err
	}
	return a.recordHeight(db, root, parent, block, updates)
}

func measureNodeUpdates(height uint64, merged *trienode.MergedNodeSet) NodeUpdateStats {
	stats := NodeUpdateStats{Height: height}
	if merged == nil {
		return stats
	}
	for _, set := range merged.Sets {
		for _, node := range set.Nodes {
			if node == nil || node.IsDeleted() {
				stats.Deletes++
				continue
			}
			stats.Nodes++
			stats.BlobBytes += uint64(len(node.Blob))
			stats.HashBytes += common.HashLength
		}
	}
	return stats
}

// OnCanonicalBlock records roots for blocks which produced no trie database
// update and therefore did not reach OnTrieDatabaseUpdate.
func (a *EpochAdapter) OnCanonicalBlock(db *trie.Database, root common.Hash, parent common.Hash, block uint64) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.hasRecordedHeight && a.lastRecordedHeight == block {
		return nil
	}
	return a.recordHeight(db, root, parent, block, nil)
}

func (a *EpochAdapter) recordHeight(db *trie.Database, root common.Hash, parent common.Hash, block uint64, updates []KeyUpdate) error {
	if err := a.ensureProcessor(block); err != nil {
		return err
	}
	if err := a.processor.RecordHeightWithParent(block, root, parent, updates); err != nil {
		return err
	}
	a.lastRecordedHeight = block
	a.hasRecordedHeight = true
	if block != a.epochEnd {
		return nil
	}
	keys, err := a.ownedFinalKeys(db, root)
	if err != nil {
		return err
	}
	result, err := a.processor.Finalize(block, keys, TriePathResolver{DB: db})
	if err != nil {
		return err
	}
	log.Info("Fountain MPT shadow epoch finalized",
		"start", a.epochStart,
		"end", a.epochEnd,
		"root", root,
		"roots", result.RootCount,
		"versions", result.VersionCount,
		"paths", result.EncodedPathCount,
		"historicalLeaves", result.HistoricalLeafCount,
		"aggregateProofs", result.AggregateProofCount,
	)
	a.processor = nil
	return nil
}

func (a *EpochAdapter) ensureProcessor(block uint64) error {
	start, end := epochBounds(block, a.config.EpochLength)
	if a.processor != nil {
		if start != a.epochStart {
			return fmt.Errorf("previous fountain MPT epoch [%d,%d] was not finalized before block %d", a.epochStart, a.epochEnd, block)
		}
		return nil
	}
	var encodedStart [8]byte
	binary.BigEndian.PutUint64(encodedStart[:], start)
	epochSeed := crypto.Keccak256(a.config.Seed, encodedStart[:])
	processor, err := NewProcessor(ProcessorConfig{
		EpochStart:     start,
		Seed:           epochSeed,
		MinimumRows:    a.config.MinimumRows,
		OwnsHistorical: func(version VersionRecord) bool { return a.ownsKey(version.Key) },
	}, a.store)
	if err != nil {
		return err
	}
	a.processor = processor
	a.epochStart = start
	a.epochEnd = end
	return nil
}

func (a *EpochAdapter) ownedFinalKeys(db *trie.Database, root common.Hash) ([][]byte, error) {
	tr, err := trie.New(trie.TrieID(root), db)
	if err != nil {
		return nil, err
	}
	iter := trie.NewIterator(tr.MustNodeIterator(nil))
	var keys [][]byte
	for iter.Next() {
		if a.ownsKey(iter.Key) {
			keys = append(keys, append([]byte(nil), iter.Key...))
		}
	}
	if iter.Err != nil {
		return nil, iter.Err
	}
	return keys, nil
}

func detectAccountUpdates(db *trie.Database, root common.Hash, states *triestate.Set, ownsKey func([]byte) bool) ([]KeyUpdate, error) {
	if states == nil || len(states.Accounts) == 0 {
		return nil, nil
	}
	tr, err := trie.New(trie.TrieID(root), db)
	if err != nil {
		return nil, err
	}
	updates := make([]KeyUpdate, 0, len(states.Accounts))
	for address, previous := range states.Accounts {
		key := crypto.Keccak256(address.Bytes())
		if ownsKey != nil && !ownsKey(key) {
			continue
		}
		current, err := tr.Get(key)
		if err != nil {
			return nil, err
		}
		updates = append(updates, KeyUpdate{
			Key:            key,
			PreviousExists: previous != nil,
			NewExists:      len(current) != 0,
		})
	}
	sort.Slice(updates, func(i, j int) bool {
		return string(updates[i].Key) < string(updates[j].Key)
	})
	return updates, nil
}

func epochBounds(block, length uint64) (uint64, uint64) {
	if block == 0 {
		return 0, 0
	}
	start := ((block - 1) / length * length) + 1
	return start, start + length - 1
}

func keyOwner(seed, key []byte, nodeCount uint64) uint64 {
	digest := crypto.Keccak256([]byte("fountainmptshadow/key-owner/v1"), seed, key)
	return binary.BigEndian.Uint64(digest[:8]) % nodeCount
}

// KeyOwner returns the unique storage node responsible for every version of
// key under a fixed seed and node set.
func KeyOwner(seed, key []byte, nodeCount uint64) uint64 {
	if nodeCount == 0 {
		return 0
	}
	return keyOwner(seed, key, nodeCount)
}

func (a *EpochAdapter) ownsKey(key []byte) bool {
	return keyOwner(a.config.Seed, key, a.config.NodeCount) == a.config.NodeIndex
}

func (r TriePathResolver) PathNodes(root common.Hash, key []byte) ([][]byte, error) {
	if r.DB == nil {
		return nil, fmt.Errorf("nil trie database")
	}
	tr, err := trie.New(trie.TrieID(root), r.DB)
	if err != nil {
		return nil, err
	}
	value, err := tr.Get(key)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("key %x not present in root %s", key, root)
	}
	var proof trienode.ProofList
	if err := tr.Prove(key, &proof); err != nil {
		return nil, err
	}
	if len(proof) == 0 {
		return nil, fmt.Errorf("empty path proof for key %x", key)
	}
	nodes := make([][]byte, len(proof))
	for i := range proof {
		nodes[i] = append([]byte(nil), proof[i]...)
	}
	return nodes, nil
}
