package partitionedmptshadow

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/internal/partitioning"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/trie/triestate"
)

// ShadowRootView adapts a committed trie root and trie database to the root
// view surface consumed by the partitioned MPT shadow processor.
type ShadowRootView struct {
	Root common.Hash
	DB   *trie.Database
}

func (view ShadowRootView) RootHashString() string {
	return view.Root.Hex()
}

func (view ShadowRootView) ExtractStatePaths() ([]partitioning.StatePath, error) {
	if view.DB == nil {
		return nil, fmt.Errorf("nil trie database")
	}
	tr, err := trie.New(trie.TrieID(view.Root), view.DB)
	if err != nil {
		return nil, err
	}
	iter := trie.NewIterator(tr.MustNodeIterator(nil))
	var paths []partitioning.StatePath
	for iter.Next() {
		path, err := view.ExtractStatePathForKey(iter.Key)
		if err != nil {
			return nil, fmt.Errorf("state path for key %x: %w", iter.Key, err)
		}
		paths = append(paths, path)
	}
	if iter.Err != nil {
		return nil, iter.Err
	}
	sort.Slice(paths, func(i, j int) bool {
		return bytes.Compare(paths[i].Key, paths[j].Key) < 0
	})
	return paths, nil
}

func (view ShadowRootView) ExtractStatePathForKey(key []byte) (partitioning.StatePath, error) {
	if view.DB == nil {
		return partitioning.StatePath{}, fmt.Errorf("nil trie database")
	}
	tr, err := trie.New(trie.TrieID(view.Root), view.DB)
	if err != nil {
		return partitioning.StatePath{}, err
	}
	value, err := tr.Get(key)
	if err != nil {
		return partitioning.StatePath{}, err
	}
	if value == nil {
		return partitioning.StatePath{}, fmt.Errorf("key %x not present in root %s", key, view.Root)
	}
	var proof trienode.ProofList
	if err := tr.Prove(key, &proof); err != nil {
		return partitioning.StatePath{}, err
	}
	if len(proof) == 0 {
		return partitioning.StatePath{}, fmt.Errorf("empty proof for key %x", key)
	}
	return statePathFromProof(key, proof)
}

func ChangedAccountStatePathsFromDirtyNodes(view ShadowRootView, nodes *trienode.MergedNodeSet) ([]partitioning.StatePath, error) {
	if nodes == nil {
		return nil, nil
	}
	accountSet := nodes.Sets[common.Hash{}]
	if accountSet == nil {
		return nil, nil
	}
	keys := make(map[string][]byte)
	var firstErr error
	accountSet.ForEachWithOrder(func(path string, node *trienode.Node) {
		if firstErr != nil || node == nil || node.IsDeleted() {
			return
		}
		key, ok, err := accountKeyFromDirtyLeaf(path, node.Blob)
		if err != nil {
			firstErr = err
			return
		}
		if ok {
			keys[string(key)] = key
		}
	})
	if firstErr != nil {
		return nil, firstErr
	}
	paths := make([]partitioning.StatePath, 0, len(keys))
	for _, key := range keys {
		path, err := view.ExtractStatePathForKey(key)
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool {
		return bytes.Compare(paths[i].Key, paths[j].Key) < 0
	})
	return paths, nil
}

// TrieDatabaseAdapter bridges trie.Database.Update inputs to Processor.
type TrieDatabaseAdapter struct {
	Processor      *Processor
	PartitionNodes PartitionNodeStore
}

func (adapter *TrieDatabaseAdapter) OnTrieDatabaseUpdate(db *trie.Database, root common.Hash, parent common.Hash, block uint64, nodes *trienode.MergedNodeSet, states *triestate.Set) error {
	_, err := adapter.OnDatabaseUpdate(db, root, parent, block, nodes, states)
	return err
}

func (adapter *TrieDatabaseAdapter) OnDatabaseUpdate(db *trie.Database, root common.Hash, parent common.Hash, block uint64, nodes *trienode.MergedNodeSet, states *triestate.Set) (*Result, error) {
	_ = states
	if adapter == nil || adapter.Processor == nil {
		return nil, fmt.Errorf("nil trie database shadow adapter")
	}
	view := ShadowRootView{Root: root, DB: db}
	changedPaths, err := ChangedAccountStatePathsFromDirtyNodes(view, nodes)
	if err != nil {
		return nil, err
	}
	if len(changedPaths) == 0 {
		log.Info("Partitioned MPT shadow update skipped", "block", block, "root", root.Hex(), "changedPaths", 0)
		return nil, nil
	}
	result, err := adapter.Processor.OnRootUpdate(Input{
		Root:           root,
		Parent:         parent,
		Block:          block,
		View:           view,
		ChangedPaths:   changedPaths,
		DirtyNodeStats: DirtyNodeStatsFromMergedNodeSet(nodes),
	})
	if err != nil {
		return nil, err
	}
	if adapter.PartitionNodes != nil {
		if err := adapter.PartitionNodes.SavePartitionNodes(result.Root, result.LocalViews, nodes); err != nil {
			return nil, err
		}
	}
	log.Info("Partitioned MPT shadow update complete",
		"block", block,
		"root", root.Hex(),
		"changedPaths", len(changedPaths),
		"partitions", len(result.PartitionResult.Partitions),
		"localViews", len(result.LocalViews),
	)
	return result, nil
}

func DirtyNodeStatsFromMergedNodeSet(nodes *trienode.MergedNodeSet) DirtyNodeStats {
	var stats DirtyNodeStats
	if nodes == nil {
		return stats
	}
	for owner, set := range nodes.Sets {
		if set == nil {
			continue
		}
		isAccountTrie := owner == (common.Hash{})
		set.ForEachWithOrder(func(_ string, node *trienode.Node) {
			if node == nil {
				return
			}
			if node.IsDeleted() {
				stats.DeletedNodeCount++
				return
			}
			size := uint64(len(node.Blob))
			stats.NodeCount++
			stats.NodeBytes += size
			if isAccountTrie {
				stats.AccountNodeCount++
				stats.AccountNodeBytes += size
			} else {
				stats.StorageNodeCount++
				stats.StorageNodeBytes += size
			}
		})
	}
	return stats
}

func accountKeyFromDirtyLeaf(path string, blob []byte) ([]byte, bool, error) {
	if len(blob) == 0 {
		return nil, false, nil
	}
	content, rest, err := rlp.SplitList(blob)
	if err != nil {
		return nil, false, fmt.Errorf("decode dirty account node: %w", err)
	}
	if len(rest) != 0 {
		return nil, false, fmt.Errorf("trailing dirty account node data")
	}
	count, err := rlp.CountValues(content)
	if err != nil {
		return nil, false, err
	}
	if count != 2 {
		return nil, false, nil
	}
	compactPath, _, err := rlp.SplitString(content)
	if err != nil {
		return nil, false, err
	}
	nibbles := compactToHex(compactPath)
	if !hasTerm(nibbles) {
		return nil, false, nil
	}
	nibbles = nibbles[:len(nibbles)-1]
	fullNibbles := append([]byte(path), nibbles...)
	if len(fullNibbles)%2 != 0 {
		return nil, false, fmt.Errorf("dirty account leaf has odd nibble key length %d", len(fullNibbles))
	}
	return hexToKeybytes(fullNibbles), true, nil
}

func statePathFromProof(key []byte, proof trienode.ProofList) (partitioning.StatePath, error) {
	nodes := make([]partitioning.PathNode, len(proof))
	hashes := make([][]byte, len(proof))
	totalSize := 0
	keyNibbles := keybytesToHexNoTerm(key)
	consumed := 0
	edgeNibble := byte(0xff)
	for i, raw := range proof {
		blob := []byte(raw)
		meta, err := decodeProofNodeMeta(blob)
		if err != nil {
			return partitioning.StatePath{}, err
		}
		hash := crypto.Keccak256Hash(blob).Bytes()
		parent := i - 1
		if i == 0 {
			parent = -1
		}
		nodes[i] = partitioning.PathNode{
			Hash:           append([]byte(nil), hash...),
			PathKey:        append([]byte(nil), key...),
			NodeType:       meta.nodeType,
			EdgeNibble:     edgeNibble,
			PrefixNibbles:  append([]byte(nil), meta.prefix...),
			Position:       i,
			ParentPosition: parent,
			ChildCount:     meta.childCount,
			Size:           len(blob),
			Raw:            append([]byte(nil), blob...),
		}
		hashes[i] = append([]byte(nil), hash...)
		totalSize += len(blob)
		if meta.nodeType == "branch" {
			if consumed < len(keyNibbles) {
				edgeNibble = keyNibbles[consumed]
				consumed++
			}
			continue
		}
		consumed += len(meta.prefix)
		edgeNibble = 0xff
	}
	return partitioning.StatePath{
		Key:         append([]byte(nil), key...),
		PathNibbles: keybytesToHexNoTerm(key),
		NodeHashes:  hashes,
		Nodes:       nodes,
		LeafHash:    hashes[len(hashes)-1],
		Size:        totalSize,
		Raw:         nodes[len(nodes)-1].Raw,
	}, nil
}

type proofNodeMeta struct {
	nodeType   string
	prefix     []byte
	childCount int
}

func decodeProofNodeMeta(blob []byte) (proofNodeMeta, error) {
	content, rest, err := rlp.SplitList(blob)
	if err != nil {
		return proofNodeMeta{}, err
	}
	if len(rest) != 0 {
		return proofNodeMeta{}, fmt.Errorf("trailing RLP data")
	}
	count, err := rlp.CountValues(content)
	if err != nil {
		return proofNodeMeta{}, err
	}
	switch count {
	case 17:
		children := 0
		remaining := content
		for i := 0; i < 16; i++ {
			_, raw, rest, err := rlp.Split(remaining)
			if err != nil {
				return proofNodeMeta{}, err
			}
			if !isRLPEmptyString(raw) {
				children++
			}
			remaining = rest
		}
		return proofNodeMeta{nodeType: "branch", childCount: children}, nil
	case 2:
		path, _, err := rlp.SplitString(content)
		if err != nil {
			return proofNodeMeta{}, err
		}
		nibbles := compactToHex(path)
		leaf := hasTerm(nibbles)
		if leaf {
			nibbles = nibbles[:len(nibbles)-1]
		}
		nodeType := "extension"
		if leaf {
			nodeType = "leaf"
		}
		return proofNodeMeta{nodeType: nodeType, prefix: nibbles}, nil
	default:
		return proofNodeMeta{}, fmt.Errorf("unexpected proof node field count %d", count)
	}
}

func compactToHex(compact []byte) []byte {
	if len(compact) == 0 {
		return compact
	}
	base := keybytesToHex(compact)
	if base[0] < 2 {
		base = base[:len(base)-1]
	}
	chop := 2 - base[0]&1
	return base[chop:]
}

func keybytesToHex(str []byte) []byte {
	l := len(str)*2 + 1
	nibbles := make([]byte, l)
	for i, b := range str {
		nibbles[i*2] = b / 16
		nibbles[i*2+1] = b % 16
	}
	nibbles[l-1] = 16
	return nibbles
}

func keybytesToHexNoTerm(key []byte) []byte {
	hex := keybytesToHex(key)
	return hex[:len(hex)-1]
}

func hexToKeybytes(hex []byte) []byte {
	if hasTerm(hex) {
		hex = hex[:len(hex)-1]
	}
	key := make([]byte, len(hex)/2)
	for bi, ni := 0, 0; ni < len(hex); bi, ni = bi+1, ni+2 {
		key[bi] = hex[ni]<<4 | hex[ni+1]
	}
	return key
}

func hasTerm(s []byte) bool {
	return len(s) > 0 && s[len(s)-1] == 16
}

func isRLPEmptyString(raw []byte) bool {
	return len(raw) == 0
}
