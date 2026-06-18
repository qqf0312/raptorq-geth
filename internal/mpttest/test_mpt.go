package mpttest

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/internal/partitioning"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

type TestTrieEntry struct {
	Name  string
	Key   []byte
	Value []byte
}

type TestMPT struct {
	Root        common.Hash
	DB          *trie.Database
	Entries     []TestTrieEntry
	LastNodeSet *trienode.NodeSet
}

func (tree *TestMPT) RootHashString() string {
	if tree == nil {
		return ""
	}
	return tree.Root.Hex()
}

func (tree *TestMPT) ExtractStatePaths() ([]partitioning.StatePath, error) {
	return ExtractStatePaths(tree)
}

func (tree *TestMPT) UpdatedNodeSet() *trienode.NodeSet {
	if tree == nil {
		return nil
	}
	return tree.LastNodeSet
}

type TestMPTNode struct {
	NodeType      string
	EdgeNibble    byte
	PrefixNibbles []byte
	Hash          []byte
	Path          []byte
	Blob          []byte
	Entry         *TestTrieEntry
	ChildCount    int
}

type formattedTrieNode struct {
	hash     string
	nodeType string
	edge     byte
	prefix   []byte
	labels   []string
	key      []byte
}

func BytesToNibbles(key []byte) []byte {
	out := make([]byte, 0, len(key)*2)
	for _, b := range key {
		out = append(out, b>>4, b&0x0f)
	}
	return out
}

func BuildOrUpdateTestMPT(prev *TestMPT, entries []TestTrieEntry) (*TestMPT, error) {
	var (
		db       *trie.Database
		parent   common.Hash
		state    map[string]TestTrieEntry
		stateKey []string
	)
	if prev == nil {
		db = trie.NewDatabase(rawdb.NewMemoryDatabase(), nil)
		parent = types.EmptyRootHash
		state = make(map[string]TestTrieEntry)
	} else {
		db = prev.DB
		parent = prev.Root
		state = make(map[string]TestTrieEntry, len(prev.Entries)+len(entries))
		for _, entry := range prev.Entries {
			key := string(entry.Key)
			state[key] = cloneEntry(entry)
			stateKey = append(stateKey, key)
		}
	}
	tr, err := trie.New(trie.TrieID(parent), db)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if len(entry.Key) == 0 {
			return nil, fmt.Errorf("entry %q has empty key", entry.Name)
		}
		if err := tr.Update(entry.Key, entry.Value); err != nil {
			return nil, err
		}
		key := string(entry.Key)
		if _, ok := state[key]; !ok {
			stateKey = append(stateKey, key)
		}
		state[key] = cloneEntry(entry)
	}
	root, nodes, err := tr.Commit(false)
	if err != nil {
		return nil, err
	}
	if nodes != nil {
		if err := db.Update(root, parent, 0, trienode.NewWithNodeSet(nodes), nil); err != nil {
			return nil, err
		}
	}
	sort.Slice(stateKey, func(i, j int) bool {
		return bytes.Compare([]byte(stateKey[i]), []byte(stateKey[j])) < 0
	})
	out := make([]TestTrieEntry, 0, len(stateKey))
	for _, key := range stateKey {
		out = append(out, cloneEntry(state[key]))
	}
	return &TestMPT{Root: root, DB: db, Entries: out, LastNodeSet: nodes}, nil
}

func BuildTestMPTFromEntries(t *testing.T, entries []TestTrieEntry) *TestMPT {
	t.Helper()
	tree, err := BuildOrUpdateTestMPT(nil, entries)
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT: %v", err)
	}
	return tree
}

func ExtractStatePathsFromTestMPT(t *testing.T, tree *TestMPT) []partitioning.StatePath {
	t.Helper()
	paths, err := ExtractStatePaths(tree)
	if err != nil {
		t.Fatalf("ExtractStatePaths: %v", err)
	}
	return paths
}

func ExtractStatePaths(tree *TestMPT) ([]partitioning.StatePath, error) {
	if tree == nil || tree.DB == nil {
		return nil, fmt.Errorf("nil test MPT")
	}
	tr, err := trie.New(trie.TrieID(tree.Root), tree.DB)
	if err != nil {
		return nil, err
	}
	labelByKey := make(map[string]TestTrieEntry, len(tree.Entries))
	for _, entry := range tree.Entries {
		labelByKey[string(entry.Key)] = cloneEntry(entry)
	}
	iter := trie.NewIterator(tr.MustNodeIterator(nil))
	var paths []partitioning.StatePath
	for iter.Next() {
		key := append([]byte(nil), iter.Key...)
		proof := iter.Prove()
		if len(proof) == 0 {
			return nil, fmt.Errorf("empty proof for key %x", key)
		}
		entry, ok := labelByKey[string(key)]
		if !ok {
			entry = TestTrieEntry{Name: fmt.Sprintf("0x%x", key), Key: key, Value: append([]byte(nil), iter.Value...)}
		}
		path, err := statePathFromProof(entry, proof)
		if err != nil {
			return nil, fmt.Errorf("path for key %x: %w", key, err)
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

func FindPathByName(paths []partitioning.StatePath, name string) (partitioning.StatePath, bool) {
	for _, path := range paths {
		if raw, ok := path.Raw.(*TestMPTNode); ok && raw.Entry != nil && raw.Entry.Name == name {
			return path, true
		}
	}
	return partitioning.StatePath{}, false
}

func LabelForPath(path partitioning.StatePath) string {
	if raw, ok := path.Raw.(*TestMPTNode); ok && raw.Entry != nil && raw.Entry.Name != "" {
		return raw.Entry.Name
	}
	return fmt.Sprintf("key=0x%x", path.Key)
}

func FormatNibbles(nibbles []byte) string {
	parts := make([]string, len(nibbles))
	for i, nibble := range nibbles {
		parts[i] = fmt.Sprintf("%x", nibble)
	}
	return strings.Join(parts, "/")
}

func FormatTrie(root any) string {
	tree, ok := root.(*TestMPT)
	if !ok || tree == nil {
		return "<unavailable real trie handle>"
	}
	paths, err := ExtractStatePaths(tree)
	if err != nil {
		return fmt.Sprintf("<extract error: %v>", err)
	}
	return FormatStatePathTrie(tree.Root, paths)
}

func FormatStatePathTrie(root common.Hash, paths []partitioning.StatePath) string {
	var out strings.Builder
	fmt.Fprintf(&out, "root hash=%s\n", shortHash(root.Bytes()))
	if len(paths) == 0 {
		return out.String()
	}
	nodes := make(map[string]*formattedTrieNode)
	children := make(map[string][]string)
	childSeen := make(map[string]map[string]bool)
	rootIDs := make([]string, 0, 1)
	rootSeen := make(map[string]bool)
	for _, path := range paths {
		for i := range path.Nodes {
			hash := string(path.Nodes[i].Hash)
			info := nodes[hash]
			if info == nil {
				info = &formattedTrieNode{hash: hash, nodeType: path.Nodes[i].NodeType, edge: path.Nodes[i].EdgeNibble, prefix: append([]byte(nil), path.Nodes[i].PrefixNibbles...)}
				nodes[hash] = info
			}
			if i == len(path.Nodes)-1 {
				label := LabelForPath(path)
				if !containsString(info.labels, label) {
					info.labels = append(info.labels, label)
				}
				if len(info.key) == 0 {
					info.key = append([]byte(nil), path.Key...)
				}
			}
			if i == 0 {
				if !rootSeen[hash] {
					rootIDs = append(rootIDs, hash)
					rootSeen[hash] = true
				}
				continue
			}
			parent := string(path.Nodes[i-1].Hash)
			if childSeen[parent] == nil {
				childSeen[parent] = make(map[string]bool)
			}
			if !childSeen[parent][hash] {
				children[parent] = append(children[parent], hash)
				childSeen[parent][hash] = true
			}
		}
	}
	sort.Slice(rootIDs, func(i, j int) bool { return bytes.Compare([]byte(rootIDs[i]), []byte(rootIDs[j])) < 0 })
	for _, ids := range children {
		sort.Slice(ids, func(i, j int) bool {
			left, right := nodes[ids[i]], nodes[ids[j]]
			if left.edge != right.edge {
				return left.edge < right.edge
			}
			return bytes.Compare([]byte(ids[i]), []byte(ids[j])) < 0
		})
	}
	visited := make(map[string]bool)
	for _, id := range rootIDs {
		formatStatePathTrieNode(&out, id, "", nodes, children, visited)
	}
	return out.String()
}

func formatStatePathTrieNode(out *strings.Builder, id string, indent string, nodes map[string]*formattedTrieNode, children map[string][]string, visited map[string]bool) {
	info := nodes[id]
	if info == nil {
		return
	}
	out.WriteString(indent)
	if info.edge <= 0x0f {
		fmt.Fprintf(out, "edge=0x%x ", info.edge)
	} else {
		out.WriteString("root ")
	}
	fmt.Fprintf(out, "%s prefix=%s hash=%s", info.nodeType, FormatNibbles(info.prefix), shortHash([]byte(id)))
	if len(info.labels) > 0 {
		sort.Strings(info.labels)
		fmt.Fprintf(out, " -> %s key=0x%x", strings.Join(info.labels, ","), info.key)
	}
	out.WriteByte('\n')
	if visited[id] {
		out.WriteString(indent)
		out.WriteString("  <cycle>\n")
		return
	}
	visited[id] = true
	for _, child := range children[id] {
		formatStatePathTrieNode(out, child, indent+"  ", nodes, children, visited)
	}
	visited[id] = false
}

func SharedNodeHashes(a, b []partitioning.StatePath) [][]byte {
	seen := make(map[string][]byte)
	for _, path := range a {
		for _, node := range path.Nodes {
			if len(node.Hash) > 0 {
				seen[string(node.Hash)] = append([]byte(nil), node.Hash...)
			}
		}
	}
	var out [][]byte
	added := make(map[string]bool)
	for _, path := range b {
		for _, node := range path.Nodes {
			key := string(node.Hash)
			if len(node.Hash) == 0 || added[key] {
				continue
			}
			if hash, ok := seen[key]; ok {
				out = append(out, append([]byte(nil), hash...))
				added[key] = true
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i], out[j]) < 0 })
	return out
}

func statePathFromProof(entry TestTrieEntry, proof [][]byte) (partitioning.StatePath, error) {
	nodes := make([]partitioning.PathNode, len(proof))
	hashes := make([][]byte, len(proof))
	totalSize := 0
	keyNibbles := BytesToNibbles(entry.Key)
	consumed := 0
	edgeNibble := byte(0xff)
	for i, blob := range proof {
		meta, err := decodeProofNode(blob)
		if err != nil {
			return partitioning.StatePath{}, err
		}
		hash := crypto.Keccak256Hash(blob).Bytes()
		parent := i - 1
		if i == 0 {
			parent = -1
		}
		raw := &TestMPTNode{
			NodeType:      meta.nodeType,
			EdgeNibble:    edgeNibble,
			PrefixNibbles: append([]byte(nil), meta.prefix...),
			Hash:          append([]byte(nil), hash...),
			Blob:          append([]byte(nil), blob...),
			Entry:         nil,
			ChildCount:    meta.childCount,
		}
		if i == len(proof)-1 {
			cloned := cloneEntry(entry)
			raw.Entry = &cloned
		}
		size := len(blob)
		nodes[i] = partitioning.PathNode{
			Hash:           append([]byte(nil), hash...),
			PathKey:        append([]byte(nil), entry.Key...),
			NodeType:       meta.nodeType,
			EdgeNibble:     edgeNibble,
			PrefixNibbles:  append([]byte(nil), meta.prefix...),
			Position:       i,
			ParentPosition: parent,
			ChildCount:     meta.childCount,
			Size:           size,
			Raw:            raw,
		}
		hashes[i] = append([]byte(nil), hash...)
		totalSize += size
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
		Key:         append([]byte(nil), entry.Key...),
		PathNibbles: BytesToNibbles(entry.Key),
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

func decodeProofNode(blob []byte) (proofNodeMeta, error) {
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
		nibbles, leaf := compactToNibbles(path)
		nodeType := "extension"
		if leaf {
			nodeType = "leaf"
		}
		return proofNodeMeta{nodeType: nodeType, prefix: nibbles}, nil
	default:
		return proofNodeMeta{}, fmt.Errorf("unexpected proof node field count %d", count)
	}
}

func compactToNibbles(compact []byte) ([]byte, bool) {
	if len(compact) == 0 {
		return nil, false
	}
	nibbles := make([]byte, 0, len(compact)*2)
	for _, b := range compact {
		nibbles = append(nibbles, b>>4, b&0x0f)
	}
	leaf := nibbles[0]&0x2 != 0
	odd := nibbles[0]&0x1 != 0
	if odd {
		nibbles = nibbles[1:]
	} else {
		nibbles = nibbles[2:]
	}
	return nibbles, leaf
}

func isRLPEmptyString(raw []byte) bool {
	return len(raw) == 0
}

func cloneEntry(entry TestTrieEntry) TestTrieEntry {
	return TestTrieEntry{
		Name:  entry.Name,
		Key:   append([]byte(nil), entry.Key...),
		Value: append([]byte(nil), entry.Value...),
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func shortHash(hash []byte) string {
	if len(hash) <= 3 {
		return hex.EncodeToString(hash)
	}
	return hex.EncodeToString(hash[:3]) + "..."
}

func formatPathHashes(nodes []partitioning.PathNode) string {
	var out strings.Builder
	for i := range nodes {
		if i > 0 {
			out.WriteString("->")
		}
		out.WriteString(shortHash(nodes[i].Hash))
	}
	return out.String()
}
