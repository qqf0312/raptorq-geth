// Copyright 2022 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package trie

import (
	"bytes"
	"errors"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie/triedb/hashdb"
	"github.com/ethereum/go-ethereum/trie/triedb/pathdb"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/trie/triestate"
	"github.com/klauspost/reedsolomon"
)

// Config defines all necessary options for database.
type Config struct {
	Preimages bool           // Flag whether the preimage of node key is recorded
	IsVerkle  bool           // Flag whether the db is holding a verkle tree
	HashDB    *hashdb.Config // Configs for hash-based scheme
	PathDB    *pathdb.Config // Configs for experimental path-based scheme
}

// HashDefaults represents a config for using hash-based scheme with
// default settings.
var HashDefaults = &Config{
	Preimages: false,
	HashDB:    hashdb.Defaults,
}

// backend defines the methods needed to access/update trie nodes in different
// state scheme.
type backend interface {
	// Scheme returns the identifier of used storage scheme.
	Scheme() string

	// Initialized returns an indicator if the state data is already initialized
	// according to the state scheme.
	Initialized(genesisRoot common.Hash) bool

	// Size returns the current storage size of the diff layers on top of the
	// disk layer and the storage size of the nodes cached in the disk layer.
	//
	// For hash scheme, there is no differentiation between diff layer nodes
	// and dirty disk layer nodes, so both are merged into the second return.
	Size() (common.StorageSize, common.StorageSize)

	// Update performs a state transition by committing dirty nodes contained
	// in the given set in order to update state from the specified parent to
	// the specified root.
	//
	// The passed in maps(nodes, states) will be retained to avoid copying
	// everything. Therefore, these maps must not be changed afterwards.
	Update(root common.Hash, parent common.Hash, block uint64, nodes *trienode.MergedNodeSet, states *triestate.Set) error

	// Commit writes all relevant trie nodes belonging to the specified state
	// to disk. Report specifies whether logs will be displayed in info level.
	Commit(root common.Hash, report bool) error

	// Close closes the trie database backend and releases all held resources.
	Close() error
}

// Database is the wrapper of the underlying backend which is shared by different
// types of node backend as an entrypoint. It's responsible for all interactions
// relevant with trie nodes and node preimages.
type Database struct {
	config    *Config        // Configuration for trie database
	diskdb    ethdb.Database // Persistent database to store the snapshot
	preimages *preimageStore // The store for caching preimages
	backend   backend        // The backend for managing trie nodes

	meta *StateMetaIndex // 冷热节点元数据索引
}

// -------------------------- 冷热节点元数据核心逻辑 --------------------------
// StateMeta 单个节点的冷热元数据（hash=节点本身的哈希）
type StateMeta struct {
	NodeHash       common.Hash // 节点本身的哈希（HashDB存储的Key）
	AccessTime     uint64      // 节点访问次数
	CreationHeight uint64      // 节点创建时的区块高度
	Timer          uint64      // 节点过期（冷节点）的区块高度
}

// StateMetaIndex 冷热元数据索引（hash=节点在树上的Path哈希）
type StateMetaIndex struct {
	mu    sync.RWMutex
	T     uint64                // 基础过期高度偏移量
	F     uint64                // 访问次数系数
	metas map[string]*StateMeta // Key=Path哈希，Value=节点元数据
	db    *Database             // 关联外层Database，仅调用公开接口
}

// NewStateMetaIndex 初始化冷热元数据索引
func NewStateMetaIndex(db *Database) *StateMetaIndex {
	return &StateMetaIndex{
		metas: make(map[string]*StateMeta),
		T:     2,
		F:     10,
		db:    db,
	}
}

// Create 初始化节点元数据（string(keyBytes)做键，直观）
// path: 节点在trie树上的路径（核心键）
// height: 创建时的区块高度
// nodeHash: 节点本身的哈希
func (idx *StateMetaIndex) Create(key string, height uint64, nodeHash common.Hash) *StateMeta {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	meta := &StateMeta{
		NodeHash:       nodeHash,
		CreationHeight: height,
		AccessTime:     0,
	}
	idx.metas[key] = meta //
	return meta
}

// Get 获取节点元数据（通过path查询）
func (idx *StateMetaIndex) Get(key string) (*StateMeta, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	meta, ok := idx.metas[key]
	return meta, ok
}

// OnAccess 记录节点访问（通过path更新）
func (idx *StateMetaIndex) OnAccess(key string, nodeHash common.Hash, height uint64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	meta, ok := idx.metas[key]
	if !ok {
		// 访问未初始化的节点，自动创建元数据（nodeHash先置空，后续可补充）
		meta = &StateMeta{
			NodeHash:       nodeHash,
			CreationHeight: height,
			AccessTime:     0,
		}
		idx.metas[key] = meta
	}
	meta.AccessTime++
}

// UpdateTimer 更新节点过期时间（通过path）
func (idx *StateMetaIndex) UpdateTimer(key string, height uint64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	meta, ok := idx.metas[key]
	if !ok {
		panic("UpdateTimer called on non-existing meta: " + (key))
	}
	val1 := height + idx.T
	val2 := meta.CreationHeight + (meta.AccessTime / idx.F)
	meta.Timer = val1
	if val2 > val1 {
		meta.Timer = val2
	}
}

// Delete 删除节点元数据（通过path）
func (idx *StateMetaIndex) Delete(key string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.metas, key)
}

// IsCold 判断节点是否为冷节点（通过path）
func (idx *StateMetaIndex) IsCold(key string, height uint64) bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	meta, ok := idx.metas[key]
	if !ok {
		panic("IsCold called on non-existing meta: " + key)
	}
	return height >= meta.Timer
}

// CollectCold 收集冷节点（返回map：path → 节点hash）
// 替代原有的两个数组，键为path，值为对应节点的hash，更直观且避免下标错位
func (idx *StateMetaIndex) CollectCold(height uint64) map[string]common.Hash {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	// 核心修改：用map替代两个数组，path做键，nodeHash做值
	coldMap := make(map[string]common.Hash)
	for key, meta := range idx.metas {
		log.Info("coldMap", "key", key, "height", height, "Timer", meta.Timer)
		if height >= meta.Timer { // 冷节点判定条件不变
			coldMap[key] = meta.NodeHash
		}
	}
	return coldMap
}

// CollectColdNodes 收集冷节点（返回map：path → *trienode.Node）
// 替代原有的两个数组，直接返回path和节点对象的映射，便于后续使用
func (idx *StateMetaIndex) CollectColdNodes(height uint64, root common.Hash) map[string]*trienode.Node {
	// 1. 先收集冷节点的path→nodeHash映射（复用修改后的CollectCold）
	coldMap := idx.CollectCold(height)
	if len(coldMap) == 0 {
		return nil
	}

	// 2. 创建Reader）
	reader, err := idx.db.Reader(root)
	if err != nil {
		log.Error("Failed to create trie reader", "root", root.Hex(), "err", err)
		return nil // 即使Reader创建失败，也返回path→hash的空节点映射（或返回nil，按需调整）
	}

	// 3. 遍历冷节点，读取数据并构建path→Node的map
	coldNodesMap := make(map[string]*trienode.Node)
	for key, hash := range coldMap {
		blob, err := reader.Node(common.Hash{}, nil, hash)
		if err != nil || len(blob) == 0 {
			log.Warn("Cold node not found", "key", key, "nodeHash", hash.Hex(), "err", err)
			continue // 读取失败则跳过该节点，不加入结果map
		}
		// 构建trienode.Node并加入map，path为键
		coldNodesMap[key] = trienode.New(hash, blob)
	}
	log.Info("The number of cold nodes", "num", len(coldNodesMap))
	return coldNodesMap
}

// BuildColdTrie 基于冷节点映射构建原生Trie实例（仅用公开接口，不修改trie核心）
// height: 当前区块高度
// root: 原状态根（用于创建Reader）
func (db *Database) BuildColdTrie(height uint64, root common.Hash) (*Trie, common.Hash, error) {
	// 1. 收集冷节点映射
	coldNodes := db.meta.CollectColdNodes(height, root)
	if len(coldNodes) == 0 {
		log.Warn("cold node 0")
		return nil, common.Hash{}, nil
	}

	// 2. 创建空Trie
	coldTrie := NewEmpty(db)

	// 3. 遍历冷节点，通过原生Update方法插入（核心复用逻辑）
	for key, node := range coldNodes {
		// 3.1 路径转换：trie path → 原生Trie的key格式（hex→keybytes）
		keyBytes := []byte(key)
		if len(keyBytes) == 0 {
			log.Warn("invalid cold node path", "keyBytes", keyBytes)
			continue
		}

		// 3.2 解析叶子节点value（从blob中提取）
		value, err := db.extractLeafValue(node.Blob)
		if err != nil {
			log.Warn("extract cold node value failed", "key", keyBytes, "err", err)
			continue
		}

		// 3.3 复用原生Trie.Update插入节点（核心：仅调用公开接口）
		if err := coldTrie.Update(keyBytes, value); err != nil {
			log.Warn("insert cold node to trie failed", "key", keyBytes, "err", err)
			continue
		}
	}

	return coldTrie, coldTrie.Hash(), nil
}

// SubTrieChunk 封装子树信息
type SubTrieChunk struct {
	RootPre []byte                 // 子树前缀
	Nodes   map[common.Hash][]byte // 子树下所有节点的哈希和对应的 RLP
	Proof   map[string][][]byte    // leafKey -> root->leafKey 的 Merkle proof
}

// NodeEntry 存储节点的哈希和对应的 RLP 数据
type rlpNodeEntry struct {
	Hash common.Hash
	Blob []byte
}

// ProofEntry 存储叶子路径和对应的默克尔证明
type rlpProofEntry struct {
	Key   []byte // 改为 []byte，RLP 处理更原生
	Proof [][]byte
}

// SubTrieChunk 现在的结构体可以被 RLP 完美序列化
type rlpSubTrieChunk struct {
	RootPre []byte
	Nodes   []rlpNodeEntry
	Proof   []rlpProofEntry
}

// SplitTrie 将大 Trie 按 k 个子树分块，返回每个子树的数据块
func (db *Database) SplitTrie(coldTrie *Trie, root common.Hash, k int) ([]*SubTrieChunk, error) {
	if k <= 0 {
		return nil, errors.New("invalid k")
	}

	// 创建数据库读取器
	// reader, err := db.Reader(root)
	// if err != nil {
	// 	return nil, err
	// }

	// BFS 扩展子根
	subPrefixes, err := db.CollectPrefixesBFS(coldTrie, k)
	if err != nil {
		return nil, err
	}

	// 构造每个子树的数据块
	var chunks []*SubTrieChunk
	for _, subPrefix := range subPrefixes {
		chunk, err := db.CollectSubTrieWithPrefix(coldTrie, subPrefix)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	log.Info("The number of chunk", "num", len(chunks))
	return chunks, nil
}

// CollectPrefixesBFS 从 root 节点开始 BFS 收集子树前缀
// 返回最多 k 个前缀
func (db *Database) CollectPrefixesBFS(tr *Trie, k int) ([][]byte, error) {
	if k <= 0 {
		return nil, errors.New("invalid k")
	}

	type queueItem struct {
		prefix []byte
		node   node
	}

	var prefixes [][]byte
	queue := []queueItem{{prefix: []byte{}, node: tr.root}}

	// 只要队列不空，且我们还没凑够 k 个分块
	for len(queue) > 0 {
		// 如果当前队列里的节点数已经足够（或者已经是我们要的 k 个）
		if len(queue) >= k {
			break 
		}

		item := queue[0]
		queue = queue[1:]

		switch n := item.node.(type) {
		case *fullNode:
			for i := 0; i < 16; i++ {
				if n.Children[i] != nil {
					newPrefix := append(append([]byte{}, item.prefix...), byte(i))
					queue = append(queue, queueItem{prefix: newPrefix, node: n.Children[i]})
				}
			}
		case *shortNode:
			newPrefix := append(append([]byte{}, item.prefix...), n.Key...)
			queue = append(queue, queueItem{prefix: newPrefix, node: n.Val})

		default:
			// 【边界条件】
			// 如果走到这里（比如是 valueNode），说明这个节点已经没有孩子了。
			// 我们不能直接丢弃它，必须把它作为这一个分支的“最终前缀”存起来。
			prefixes = append(prefixes, item.prefix)
			// 注意：此时我们减少了队列长度，但增加了一个确定前缀
		}
	}

	// 最后，把留在队列里的所有候选节点也加入 prefixes
	for _, item := range queue {
		prefixes = append(prefixes, item.prefix)
	}

	return prefixes, nil
}


// CollectSubTrieWithPrefix 基于迭代器收集指定前缀的子树（核心实现）
// tr: 原始Trie实例
// subPrefix: 子树前缀（nibble路径，来自CollectPrefixesBFS）
func (db *Database) CollectSubTrieWithPrefix(tr *Trie, subPrefix []byte) (*SubTrieChunk, error) {
	if tr == nil || len(subPrefix) == 0 {
		return nil, errors.New("invalid trie or subPrefix")
	}

	// 1. 创建节点迭代器，并seek到subPrefix对应的子树
	nodeIt, err := tr.NodeIterator(subPrefix)
	if err != nil {
		return nil, err
	}

	// 2. 初始化返回结果
	chunk := &SubTrieChunk{
		RootPre: subPrefix,
		Nodes:   make(map[common.Hash][]byte), // 子树所有节点（hash→RLP）
		Proof:   make(map[string][][]byte),    // 叶子节点proof（leafKey→proof）
	}

	// 3. 遍历子树所有节点（仅遍历subPrefix子树内的节点）
	for nodeIt.Next(true) {
		// 3.1 检查当前节点路径是否以subPrefix为前缀（防止越界）
		currentPath := nodeIt.Path()
		if !bytes.HasPrefix(currentPath, subPrefix) {
			break // 超出子树范围，终止遍历
		}

		// 3.2 收集节点（仅收集有独立哈希的节点）
		nodeHash := nodeIt.Hash()
		if nodeHash != (common.Hash{}) { // 跳过内嵌节点（无独立哈希）
			nodeBlob := nodeIt.NodeBlob()
			if len(nodeBlob) > 0 {
				chunk.Nodes[nodeHash] = nodeBlob
			}
		}

		// 3.3 收集叶子节点的Merkle证明
		if nodeIt.Leaf() {
			leafKey := hexToKeybytes(nodeIt.LeafKey()) // 转换为原始key
			chunk.Proof[string(leafKey)] = nodeIt.LeafProof()
		}
	}

	// 4. 检查迭代器错误
	if err := nodeIt.Error(); err != nil && err != errIteratorEnd {
		return nil, err
	}

	return chunk, nil
}

func EncodeSubTrieChunk(chunks []*SubTrieChunk) ([][]byte, error) {
	k := len(chunks) // 叶子节点数量
	enc, err := reedsolomon.New(k, k)
	if err != nil {
		return nil, err
	}
	var (
		serializedChunks        = make([][]byte, len(chunks))
		maxLen           uint64 = 0 // 最长序列化长度
	)
	for i, chunk := range chunks {
		// --- 局部转换逻辑开始 ---
		// 将你的 map 转换为有序的 slice（或者直接转换）
		// 为了保证 RS 编码在不同机器上的一致性，建议对 Map 的 Key 进行排序
		// 但如果你的 Map 是刚刚通过迭代器生成的，顺序通常是确定的
		
		rlpChunk := rlpSubTrieChunk{
			RootPre: chunk.RootPre,
			Nodes:   make([]rlpNodeEntry, 0, len(chunk.Nodes)),
			Proof:   make([]rlpProofEntry, 0, len(chunk.Proof)),
		}

		// 转换 Nodes map
		for h, b := range chunk.Nodes {
			rlpChunk.Nodes = append(rlpChunk.Nodes, rlpNodeEntry{Hash: h, Blob: b})
		}
		// 转换 Proof map
		for key, p := range chunk.Proof {
			rlpChunk.Proof = append(rlpChunk.Proof, rlpProofEntry{Key: []byte(key), Proof: p})
		}
		// --- 局部转换逻辑结束 ---

		// 使用适配后的结构体进行 RLP 编码
		serialized, err := rlp.EncodeToBytes(rlpChunk)
		if err != nil {
			return nil, err
		}
		serializedChunks[i] = serialized

		// 更新最长长度
		if uint64(len(serialized)) > maxLen {
			maxLen = uint64(len(serialized))
		}
	}

	alignedChunks := make([][]byte, len(chunks))
	for i, serialized := range serializedChunks {
		// 创建补零后的字节数组（长度=maxLen）
		aligned := make([]byte, maxLen)
		// 拷贝序列化数据，不足部分自动补零
		copy(aligned, serialized)
		alignedChunks[i] = aligned
	}

	// Reed-Solomon编码生成冗余数据（k个数据块生成k个冗余块）
	output := make([][]byte, k+k)
	for i := 0; i < k+k; i++ {
		// 所有的分片（无论数据还是校验）都必须长度一致
		output[i] = make([]byte, maxLen)
	}
	copy(output[:k], alignedChunks)
	if err := enc.Encode(output); err != nil {
		return nil, err
	}
	return output, nil
}

func WriteSubTrieChunkToDisk(db ethdb.Database, root common.Hash, chunks [][]byte) error {

	batch := db.NewBatch()
	if batch == nil {
		return errors.New("failed to create ethdb batch")
	}
	for i, chunk := range chunks {
		if len(chunk) == 0 {
			log.Warn("Skipping empty sub trie chunk", "root", root.Hex(), "index", i)
			continue
		}
		rawdb.WriteColdTrieNode(batch, root.Bytes(), uint64(i), chunk)
	}

	if err := batch.Write(); err != nil {
		return err
	}
	batch.Reset()

	log.Info("Success write subTrieChunkToDisk");
	return nil
}

// NewDatabase initializes the trie database with default settings, note
// the legacy hash-based scheme is used by default.
func NewDatabase(diskdb ethdb.Database, config *Config) *Database {
	// Sanitize the config and use the default one if it's not specified.
	if config == nil {
		config = HashDefaults
	}
	var preimages *preimageStore
	if config.Preimages {
		preimages = newPreimageStore(diskdb)
	}
	db := &Database{
		config:    config,
		diskdb:    diskdb,
		preimages: preimages,
	}
	if config.HashDB != nil && config.PathDB != nil {
		log.Crit("Both 'hash' and 'path' mode are configured")
	}
	if config.PathDB != nil {
		db.backend = pathdb.New(diskdb, config.PathDB)
	} else {
		db.backend = hashdb.New(diskdb, config.HashDB, mptResolver{})
	}
	// 修正：传入Database本身，而非backend
	db.meta = NewStateMetaIndex(db)
	return db
}

// Reader returns a reader for accessing all trie nodes with provided state root.
// An error will be returned if the requested state is not available.
func (db *Database) Reader(blockRoot common.Hash) (Reader, error) {
	switch b := db.backend.(type) {
	case *hashdb.Database:
		return b.Reader(blockRoot)
	case *pathdb.Database:
		return b.Reader(blockRoot)
	}
	return nil, errors.New("unknown backend")
}

// AccessAddr 简化版：仅初始化/更新元数据，无冷热迁移逻辑
// path: 账户树叶子节点path
// h: 当前区块高度
// nodeHash: 节点哈希
func (db *Database) AccessAddr(key string, h uint64, nodeHash common.Hash) {
	// 1. 初始化元数据（仅新增节点）
	if _, exists := db.meta.Get(key); !exists {
		db.meta.Create(key, h, nodeHash)
	}
	// 2. 更新访问次数
	db.meta.OnAccess(key, nodeHash, h)
	// 3. 计算冷节点阈值
	db.meta.UpdateTimer(key, h)

	// 日志仅保留核心信息，避免冗余
	meta, _ := db.meta.Get(key)
	log.Info("节点元数据更新完成", "keyBytes", common.Bytes2Hex([]byte(key)), "block", h, "accessTime", meta.AccessTime, "coldThreshold", meta.Timer)
}

// IsLeafNode 基于原生decodeNode精准判断是否为叶子节点
func IsLeafNode(blob []byte) bool {
	if len(blob) == 0 {
		return false
	}
	// 1. 调用原生decodeNode解析Blob
	n, err := decodeNode(nil, blob)
	if err != nil {
		log.Trace("decode node blob failed when check leaf node", "err", err)
		return false
	}
	// 2. 类型断言：叶子节点最终解析为*shortNode，且其Val是valueNode
	shortNode, ok := n.(*shortNode)
	if !ok {
		return false
	}
	// 3. 最终校验：shortNode的Val必须是valueNode（而非hashNode等）
	_, isValueNode := shortNode.Val.(valueNode)
	return isValueNode
}

// extractLeafValue 基于原生decodeNode解析叶子节点value（官方标准方式）
func (db *Database) extractLeafValue(blob []byte) ([]byte, error) {
	if len(blob) == 0 {
		return nil, errors.New("empty leaf node blob")
	}
	// 1. 调用原生decodeNode解析Blob为node对象（官方标准解析逻辑）
	// hash传空即可，decodeNodeUnsafe不依赖hash做解析
	n, err := decodeNode(nil, blob)
	if err != nil {
		return nil, err
	}
	// 2. 类型断言：叶子节点最终解析为*shortNode，其Val是valueNode
	shortNode, ok := n.(*shortNode)
	if !ok {
		return nil, errors.New("node is not a shortNode (leaf node)")
	}
	// 3. 提取valueNode（叶子节点的实际value）
	valueNode, ok := shortNode.Val.(valueNode)
	if !ok {
		return nil, errors.New("shortNode Val is not a valueNode")
	}
	// 4. 转换为[]byte返回
	return []byte(valueNode), nil
}

// Update performs a state transition by committing dirty nodes contained in the
// given set in order to update state from the specified parent to the specified
// root. The held pre-images accumulated up to this point will be flushed in case
// the size exceeds the threshold.
//
// The passed in maps(nodes, states) will be retained to avoid copying everything.
// Therefore, these maps must not be changed afterwards.
func (db *Database) Update(root common.Hash, parent common.Hash, block uint64, nodes *trienode.MergedNodeSet, states *triestate.Set) error {
	if db.preimages != nil {
		db.preimages.commit(false)
	}

	// 1. 原有底层更新逻辑（完全保留，不修改）
	if err := db.backend.Update(root, parent, block, nodes, states); err != nil {
		return err
	}

	// 2. 仅处理账户树叶子节点的元数据统计
	accountOwner := common.Hash{} // 账户树固定owner（空哈希）
	accountSubset, ok := nodes.Sets[accountOwner]
	if !ok {
		log.Debug("本次更新无账户树节点", "block", block)
		return nil
	}

	// 3. 遍历账户树节点，仅处理叶子节点的元数据
	accountSubset.ForEachWithOrder(func(path string, n *trienode.Node) {

		// 仅处理叶子节点
		if !IsLeafNode(n.Blob) {
			log.Trace("跳过非叶子节点", "path", path)
			return
		}
		decodedNode, err := decodeNode(nil, n.Blob)
		if err != nil {
			log.Warn("failed to decode node", "path", path, "err", err)
			return
		}

		sn, ok := decodedNode.(*shortNode)
		if !ok {
			log.Warn("decoded node is not shortNode", "path", path)
			return
		}
		parentNibbles := []byte(path)
		fullNibbles := append(parentNibbles, sn.Key...)

		if len(fullNibbles) == 65 && fullNibbles[64] == 16 {
			fullNibbles = fullNibbles[:64] // 去掉终止符
		}
		if(len(fullNibbles) % 2 != 0){
			log.Warn("fullNibbles len not odd", "len", len(fullNibbles))
		}
		leafKeyNibbles := fullNibbles
		// 5. 转成原生 key bytes
		leafKeyBytes := hexToKeybytes(leafKeyNibbles)

		key := string(leafKeyBytes) // 以原生 key bytes 作为元数据的键，更直观
		// 跳过已删除节点，清理对应元数据
		if n.IsDeleted() {
			db.meta.Delete(key)
			log.Trace("清理删除节点元数据", "key", key)
			return
		}

		// 核心：更新节点元数据（无迁移，仅统计）
		db.AccessAddr(key, block, n.Hash)
	})

	tr, coldTrRoot, err := db.BuildColdTrie(block, root)
	if err != nil {
		return err
	}
	if tr == nil {
		log.Warn("构建冷节点Trie失败，跳过分块写入", "block", block, "root", root.Hex())
		return nil // 构建失败则跳过分块写入，但不影响正常更新流程
	}
	chunks, err := db.SplitTrie(tr, coldTrRoot, 2)
	if err != nil {
		return err
	}
	encodedChunks, err := EncodeSubTrieChunk(chunks)
	if err != nil {
		return err
	}
	if err := WriteSubTrieChunkToDisk(db.diskdb, coldTrRoot, encodedChunks); err != nil {
		return err
	}
	tr.Reset()

	return nil
}

// Commit iterates over all the children of a particular node, writes them out
// to disk. As a side effect, all pre-images accumulated up to this point are
// also written.
func (db *Database) Commit(root common.Hash, report bool) error {
	if db.preimages != nil {
		db.preimages.commit(true)
	}
	return db.backend.Commit(root, report)
}

// Size returns the storage size of diff layer nodes above the persistent disk
// layer, the dirty nodes buffered within the disk layer, and the size of cached
// preimages.
func (db *Database) Size() (common.StorageSize, common.StorageSize, common.StorageSize) {
	var (
		diffs, nodes common.StorageSize
		preimages    common.StorageSize
	)
	diffs, nodes = db.backend.Size()
	if db.preimages != nil {
		preimages = db.preimages.size()
	}
	return diffs, nodes, preimages
}

// Initialized returns an indicator if the state data is already initialized
// according to the state scheme.
func (db *Database) Initialized(genesisRoot common.Hash) bool {
	return db.backend.Initialized(genesisRoot)
}

// Scheme returns the node scheme used in the database.
func (db *Database) Scheme() string {
	return db.backend.Scheme()
}

// Close flushes the dangling preimages to disk and closes the trie database.
// It is meant to be called when closing the blockchain object, so that all
// resources held can be released correctly.
func (db *Database) Close() error {
	db.WritePreimages()
	return db.backend.Close()
}

// WritePreimages flushes all accumulated preimages to disk forcibly.
func (db *Database) WritePreimages() {
	if db.preimages != nil {
		db.preimages.commit(true)
	}
}

// Preimage retrieves a cached trie node pre-image from memory. If it cannot be
// found cached, the method queries the persistent database for the content.
func (db *Database) Preimage(hash common.Hash) []byte {
	if db.preimages == nil {
		return nil
	}
	return db.preimages.preimage(hash)
}

// Cap iteratively flushes old but still referenced trie nodes until the total
// memory usage goes below the given threshold. The held pre-images accumulated
// up to this point will be flushed in case the size exceeds the threshold.
//
// It's only supported by hash-based database and will return an error for others.
func (db *Database) Cap(limit common.StorageSize) error {
	hdb, ok := db.backend.(*hashdb.Database)
	if !ok {
		return errors.New("not supported")
	}
	if db.preimages != nil {
		db.preimages.commit(false)
	}
	return hdb.Cap(limit)
}

// Reference adds a new reference from a parent node to a child node. This function
// is used to add reference between internal trie node and external node(e.g. storage
// trie root), all internal trie nodes are referenced together by database itself.
//
// It's only supported by hash-based database and will return an error for others.
func (db *Database) Reference(root common.Hash, parent common.Hash) error {
	hdb, ok := db.backend.(*hashdb.Database)
	if !ok {
		return errors.New("not supported")
	}
	hdb.Reference(root, parent)
	return nil
}

// Dereference removes an existing reference from a root node. It's only
// supported by hash-based database and will return an error for others.
func (db *Database) Dereference(root common.Hash) error {
	hdb, ok := db.backend.(*hashdb.Database)
	if !ok {
		return errors.New("not supported")
	}
	hdb.Dereference(root)
	return nil
}

// Recover rollbacks the database to a specified historical point. The state is
// supported as the rollback destination only if it's canonical state and the
// corresponding trie histories are existent. It's only supported by path-based
// database and will return an error for others.
func (db *Database) Recover(target common.Hash) error {
	pdb, ok := db.backend.(*pathdb.Database)
	if !ok {
		return errors.New("not supported")
	}
	return pdb.Recover(target, &trieLoader{db: db})
}

// Recoverable returns the indicator if the specified state is enabled to be
// recovered. It's only supported by path-based database and will return an
// error for others.
func (db *Database) Recoverable(root common.Hash) (bool, error) {
	pdb, ok := db.backend.(*pathdb.Database)
	if !ok {
		return false, errors.New("not supported")
	}
	return pdb.Recoverable(root), nil
}

// Disable deactivates the database and invalidates all available state layers
// as stale to prevent access to the persistent state, which is in the syncing
// stage.
//
// It's only supported by path-based database and will return an error for others.
func (db *Database) Disable() error {
	pdb, ok := db.backend.(*pathdb.Database)
	if !ok {
		return errors.New("not supported")
	}
	return pdb.Disable()
}

// Enable activates database and resets the state tree with the provided persistent
// state root once the state sync is finished.
func (db *Database) Enable(root common.Hash) error {
	pdb, ok := db.backend.(*pathdb.Database)
	if !ok {
		return errors.New("not supported")
	}
	return pdb.Enable(root)
}

// Journal commits an entire diff hierarchy to disk into a single journal entry.
// This is meant to be used during shutdown to persist the snapshot without
// flattening everything down (bad for reorgs). It's only supported by path-based
// database and will return an error for others.
func (db *Database) Journal(root common.Hash) error {
	pdb, ok := db.backend.(*pathdb.Database)
	if !ok {
		return errors.New("not supported")
	}
	return pdb.Journal(root)
}

// SetBufferSize sets the node buffer size to the provided value(in bytes).
// It's only supported by path-based database and will return an error for
// others.
func (db *Database) SetBufferSize(size int) error {
	pdb, ok := db.backend.(*pathdb.Database)
	if !ok {
		return errors.New("not supported")
	}
	return pdb.SetBufferSize(size)
}

// IsVerkle returns the indicator if the database is holding a verkle tree.
func (db *Database) IsVerkle() bool {
	return db.config.IsVerkle
}
