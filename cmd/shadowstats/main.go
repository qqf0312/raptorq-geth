package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/internal/partitionedmptshadow"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

var shadowResultPrefix = []byte("partition-shadow/result/")

type datadirFlags []string

func (f *datadirFlags) String() string {
	return strings.Join(*f, ",")
}

func (f *datadirFlags) Set(value string) error {
	*f = append(*f, value)
	return nil
}

type shadowResultRecord struct {
	node  string
	path  string
	key   []byte
	value []byte
	data  partitionedmptshadow.StoredResult
}

type blockRow struct {
	Node            string
	Block           uint64
	Root            common.Hash
	PartitionID     uint64
	NewRawNodes     int
	RawNodeBytes    int
	MissingRawNodes int
	AggregatedNodes int
	CommitmentBytes int
	DirtyStats      partitionedmptshadow.DirtyNodeStats
}

type nodeSummary struct {
	Blocks          int
	NewRawNodes     int
	RawNodeBytes    int
	MissingRawNodes int
	AggregatedNodes int
	CommitmentBytes int
	DirtyNodeCount  uint64
	DirtyNodeBytes  uint64
}

func main() {
	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(io.Discard, log.LevelCrit, false)))

	var datadirs datadirFlags
	var outPath string
	flag.Var(&datadirs, "datadir", "node datadir, e.g. data/node1; may be repeated")
	flag.StringVar(&outPath, "out", "", "optional CSV output path; defaults to stdout")
	flag.Parse()

	if len(datadirs) == 0 {
		datadirs = datadirFlags{"data/node1", "data/node2", "data/node3", "data/node4"}
	}

	var out io.Writer = os.Stdout
	var file *os.File
	var err error
	if outPath != "" {
		file, err = os.Create(outPath)
		if err != nil {
			fatalf("create output: %v", err)
		}
		defer file.Close()
		out = file
	}

	writer := csv.NewWriter(out)
	defer writer.Flush()
	writeCSV(writer, []string{
		"node",
		"block",
		"root",
		"partition",
		"new_raw_nodes",
		"raw_node_bytes",
		"missing_raw_nodes",
		"aggregated_nodes",
		"commitment_bytes",
		"dirty_node_count",
		"dirty_node_bytes",
		"dirty_account_node_count",
		"dirty_account_node_bytes",
		"dirty_storage_node_count",
		"dirty_storage_node_bytes",
		"dirty_deleted_node_count",
	})

	summaries := make(map[string]*nodeSummary)
	for _, datadir := range datadirs {
		if err := scanNode(datadir, writer, summaries); err != nil {
			fatalf("%s: %v", datadir, err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		fatalf("write csv: %v", err)
	}

	fmt.Fprintln(os.Stderr, "summary")
	nodes := make([]string, 0, len(summaries))
	for node := range summaries {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	for _, node := range nodes {
		printSummary(os.Stderr, node, summaries[node])
	}
}

func scanNode(datadir string, writer *csv.Writer, summaries map[string]*nodeSummary) error {
	dbPath := filepath.Join(datadir, "geth", "chaindata")
	db, err := rawdb.Open(rawdb.OpenOptions{
		Directory: dbPath,
		Cache:     64,
		Handles:   64,
		ReadOnly:  true,
	})
	if err != nil {
		return fmt.Errorf("open chaindata %s: %w (stop nodes before scanning if the database is locked)", dbPath, err)
	}
	defer db.Close()
	triedb := trie.NewDatabase(db, trie.HashDefaults)

	nodeName := filepath.Base(datadir)
	records, err := readShadowResults(nodeName, db)
	if err != nil {
		return err
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].data.Block != records[j].data.Block {
			return records[i].data.Block < records[j].data.Block
		}
		return records[i].data.Root.Cmp(records[j].data.Root) < 0
	})

	seenRaw := make(map[common.Hash]bool)
	summary := summaries[nodeName]
	if summary == nil {
		summary = new(nodeSummary)
		summaries[nodeName] = summary
	}
	for _, record := range records {
		rows, err := rowsForResult(nodeName, db, triedb, record.data, seenRaw)
		if err != nil {
			return fmt.Errorf("block %d root %s: %w", record.data.Block, record.data.Root, err)
		}
		if len(rows) > 0 {
			summary.Blocks++
		}
		for _, row := range rows {
			writeCSV(writer, []string{
				row.Node,
				strconv.FormatUint(row.Block, 10),
				row.Root.Hex(),
				strconv.FormatUint(row.PartitionID, 10),
				strconv.Itoa(row.NewRawNodes),
				strconv.Itoa(row.RawNodeBytes),
				strconv.Itoa(row.MissingRawNodes),
				strconv.Itoa(row.AggregatedNodes),
				strconv.Itoa(row.CommitmentBytes),
				strconv.FormatUint(row.DirtyStats.NodeCount, 10),
				strconv.FormatUint(row.DirtyStats.NodeBytes, 10),
				strconv.FormatUint(row.DirtyStats.AccountNodeCount, 10),
				strconv.FormatUint(row.DirtyStats.AccountNodeBytes, 10),
				strconv.FormatUint(row.DirtyStats.StorageNodeCount, 10),
				strconv.FormatUint(row.DirtyStats.StorageNodeBytes, 10),
				strconv.FormatUint(row.DirtyStats.DeletedNodeCount, 10),
			})
			addSummary(summary, row)
		}
	}
	return nil
}

func readShadowResults(node string, db ethdb.Database) ([]shadowResultRecord, error) {
	iter := db.NewIterator(shadowResultPrefix, nil)
	defer iter.Release()

	var records []shadowResultRecord
	for iter.Next() {
		key := append([]byte(nil), iter.Key()...)
		value := append([]byte(nil), iter.Value()...)
		var result partitionedmptshadow.StoredResult
		if err := rlp.DecodeBytes(value, &result); err != nil {
			return nil, fmt.Errorf("decode shadow result %x: %w", key, err)
		}
		records = append(records, shadowResultRecord{
			node:  node,
			key:   key,
			value: value,
			data:  result,
		})
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return records, nil
}

func rowsForResult(node string, db ethdb.Database, triedb *trie.Database, result partitionedmptshadow.StoredResult, seenRaw map[common.Hash]bool) ([]blockRow, error) {
	if len(result.PartitionIDs) != len(result.RawSetKeys) || len(result.PartitionIDs) != len(result.LocalViewKeys) {
		return nil, fmt.Errorf("partition/key count mismatch ids=%d raw=%d local=%d", len(result.PartitionIDs), len(result.RawSetKeys), len(result.LocalViewKeys))
	}
	rows := make([]blockRow, 0, len(result.PartitionIDs))
	for i, partitionID := range result.PartitionIDs {
		rawSet, err := readStoredRawSet(db, result.RawSetKeys[i])
		if err != nil {
			return nil, fmt.Errorf("read rawset partition %d: %w", partitionID, err)
		}
		localView, err := readStoredLocalView(db, result.LocalViewKeys[i])
		if err != nil {
			return nil, fmt.Errorf("read localview partition %d: %w", partitionID, err)
		}
		proofSizes, err := proofNodeSizesByHash(triedb, result.Root, proofPathKeys(result, localView))
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s block=%d root=%s partition=%d proof unavailable: %v\n", node, result.Block, result.Root, partitionID, err)
			proofSizes = make(map[common.Hash]int)
		}
		row := blockRow{
			Node:        node,
			Block:       result.Block,
			Root:        result.Root,
			PartitionID: partitionID,
			DirtyStats:  storedDirtyNodeStats(result),
		}
		for _, hashText := range rawSet.Hashes {
			hash := common.HexToHash(hashText)
			if seenRaw[hash] {
				continue
			}
			seenRaw[hash] = true
			if size := storedRawNodeSize(rawSet, hash); size > 0 {
				row.NewRawNodes++
				row.RawNodeBytes += size
				continue
			}
			blob := rawdb.ReadLegacyTrieNode(db, hash)
			if len(blob) == 0 {
				if proofSize := proofSizes[hash]; proofSize > 0 {
					row.NewRawNodes++
					row.RawNodeBytes += proofSize
					continue
				}
				row.MissingRawNodes++
				continue
			}
			row.NewRawNodes++
			row.RawNodeBytes += len(blob)
		}
		for _, agg := range localView.AggregatedNodes {
			row.AggregatedNodes++
			row.CommitmentBytes += len(agg.Commitment)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func storedRawNodeSize(rawSet *partitionedmptshadow.StoredRawSet, hash common.Hash) int {
	if rawSet == nil {
		return 0
	}
	for _, node := range rawSet.Nodes {
		if common.BytesToHash(node.Hash) == hash {
			return int(node.BlobSize)
		}
	}
	return 0
}

func storedDirtyNodeStats(result partitionedmptshadow.StoredResult) partitionedmptshadow.DirtyNodeStats {
	if len(result.DirtyNodeStats) == 0 {
		return partitionedmptshadow.DirtyNodeStats{}
	}
	return result.DirtyNodeStats[0]
}

func proofPathKeys(result partitionedmptshadow.StoredResult, localView *partitionedmptshadow.StoredLocalView) [][]byte {
	seen := make(map[string]bool)
	var keys [][]byte
	add := func(key []byte) {
		if len(key) == 0 {
			return
		}
		id := string(key)
		if seen[id] {
			return
		}
		seen[id] = true
		keys = append(keys, append([]byte(nil), key...))
	}
	for _, key := range result.ChangedPathKeys {
		add(key)
	}
	if localView == nil {
		return keys
	}
	for _, key := range localView.RawPathKeys {
		add(key)
	}
	for _, key := range localView.PreservedPathKeys {
		add(key)
	}
	for _, agg := range localView.AggregatedNodes {
		add(agg.PathKey)
		for _, attached := range agg.AttachedRawNodes {
			add(attached.PathKey)
		}
	}
	return keys
}

func proofNodeSizesByHash(triedb *trie.Database, root common.Hash, pathKeys [][]byte) (map[common.Hash]int, error) {
	sizes := make(map[common.Hash]int)
	if len(pathKeys) == 0 {
		return sizes, nil
	}
	tr, err := trie.New(trie.TrieID(root), triedb)
	if err != nil {
		return nil, err
	}
	for _, key := range pathKeys {
		var proof trienode.ProofList
		if err := tr.Prove(key, &proof); err != nil {
			return nil, fmt.Errorf("prove key %x: %w", key, err)
		}
		for _, raw := range proof {
			blob := []byte(raw)
			hash := crypto.Keccak256Hash(blob)
			sizes[hash] = len(blob)
		}
	}
	return sizes, nil
}

func readStoredRawSet(db ethdb.Database, key []byte) (*partitionedmptshadow.StoredRawSet, error) {
	blob, err := db.Get(key)
	if err != nil {
		return nil, err
	}
	var rawSet partitionedmptshadow.StoredRawSet
	if err := rlp.DecodeBytes(blob, &rawSet); err != nil {
		return nil, err
	}
	return &rawSet, nil
}

func readStoredLocalView(db ethdb.Database, key []byte) (*partitionedmptshadow.StoredLocalView, error) {
	blob, err := db.Get(key)
	if err != nil {
		return nil, err
	}
	var localView partitionedmptshadow.StoredLocalView
	if err := rlp.DecodeBytes(blob, &localView); err != nil {
		return nil, err
	}
	return &localView, nil
}

func addSummary(summary *nodeSummary, row blockRow) {
	summary.NewRawNodes += row.NewRawNodes
	summary.RawNodeBytes += row.RawNodeBytes
	summary.MissingRawNodes += row.MissingRawNodes
	summary.AggregatedNodes += row.AggregatedNodes
	summary.CommitmentBytes += row.CommitmentBytes
	summary.DirtyNodeCount += row.DirtyStats.NodeCount
	summary.DirtyNodeBytes += row.DirtyStats.NodeBytes
}

func printSummary(w io.Writer, label string, summary *nodeSummary) {
	fmt.Fprintf(w, "%s blocks=%d new_raw_nodes=%d raw_node_bytes=%d missing_raw_nodes=%d aggregated_nodes=%d commitment_bytes=%d dirty_node_count=%d dirty_node_bytes=%d\n",
		label,
		summary.Blocks,
		summary.NewRawNodes,
		summary.RawNodeBytes,
		summary.MissingRawNodes,
		summary.AggregatedNodes,
		summary.CommitmentBytes,
		summary.DirtyNodeCount,
		summary.DirtyNodeBytes,
	)
}

func writeCSV(writer *csv.Writer, row []string) {
	if err := writer.Write(row); err != nil {
		fatalf("write csv: %v", err)
	}
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "shadowstats: "+format+"\n", args...)
	os.Exit(1)
}
