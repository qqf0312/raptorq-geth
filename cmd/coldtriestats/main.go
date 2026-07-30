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

	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/internal/coldtrieshadow"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
)

var coldTrieStatsPrefix = []byte("coldtrie-stats-")

type datadirFlags []string

func (f *datadirFlags) String() string {
	return strings.Join(*f, ",")
}

func (f *datadirFlags) Set(value string) error {
	*f = append(*f, value)
	return nil
}

type statsRecord struct {
	node string
	data coldtrieshadow.StoredColdTrieStats
}

type nodeSummary struct {
	Blocks       int
	ColdKeys     uint64
	NodeCount    uint64
	NodeBytes    uint64
	PathCount    uint64
	PathBytesSum uint64
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
		"state_root",
		"cold_root",
		"cold_keys",
		"node_count",
		"node_bytes",
		"path_count",
		"path_bytes_sum",
		"avg_path_bytes",
		"threshold",
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
	dbPath := filepath.Join(datadir, "geth", "coldtriedata")
	db, err := rawdb.Open(rawdb.OpenOptions{
		Directory: dbPath,
		Cache:     64,
		Handles:   64,
		ReadOnly:  true,
	})
	if err != nil {
		return fmt.Errorf("open coldtriedata %s: %w (stop nodes before scanning if the database is locked)", dbPath, err)
	}
	defer db.Close()

	nodeName := filepath.Base(datadir)
	records, err := readStatsRecords(nodeName, db)
	if err != nil {
		return err
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].data.BlockNumber != records[j].data.BlockNumber {
			return records[i].data.BlockNumber < records[j].data.BlockNumber
		}
		return records[i].data.ColdRoot.Cmp(records[j].data.ColdRoot) < 0
	})

	summary := summaries[nodeName]
	if summary == nil {
		summary = new(nodeSummary)
		summaries[nodeName] = summary
	}
	for _, record := range records {
		stats := record.data
		avgPathBytes := uint64(0)
		if stats.PathCount > 0 {
			avgPathBytes = stats.PathBytesSum / stats.PathCount
		}
		writeCSV(writer, []string{
			record.node,
			strconv.FormatUint(stats.BlockNumber, 10),
			stats.StateRoot.Hex(),
			stats.ColdRoot.Hex(),
			strconv.FormatUint(stats.ColdKeys, 10),
			strconv.FormatUint(stats.NodeCount, 10),
			strconv.FormatUint(stats.NodeBytes, 10),
			strconv.FormatUint(stats.PathCount, 10),
			strconv.FormatUint(stats.PathBytesSum, 10),
			strconv.FormatUint(avgPathBytes, 10),
			strconv.FormatUint(stats.Threshold, 10),
		})
		addSummary(summary, stats)
	}
	return nil
}

func readStatsRecords(node string, db ethdb.Database) ([]statsRecord, error) {
	iter := db.NewIterator(coldTrieStatsPrefix, nil)
	defer iter.Release()

	var records []statsRecord
	for iter.Next() {
		var stats coldtrieshadow.StoredColdTrieStats
		if err := rlp.DecodeBytes(iter.Value(), &stats); err != nil {
			return nil, fmt.Errorf("decode cold trie stats %x: %w", iter.Key(), err)
		}
		records = append(records, statsRecord{
			node: node,
			data: stats,
		})
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return records, nil
}

func addSummary(summary *nodeSummary, stats coldtrieshadow.StoredColdTrieStats) {
	summary.Blocks++
	summary.ColdKeys += stats.ColdKeys
	summary.NodeCount += stats.NodeCount
	summary.NodeBytes += stats.NodeBytes
	summary.PathCount += stats.PathCount
	summary.PathBytesSum += stats.PathBytesSum
}

func printSummary(w io.Writer, label string, summary *nodeSummary) {
	avgPathBytes := uint64(0)
	if summary.PathCount > 0 {
		avgPathBytes = summary.PathBytesSum / summary.PathCount
	}
	fmt.Fprintf(w, "%s blocks=%d cold_keys=%d node_count=%d node_bytes=%d path_count=%d path_bytes_sum=%d avg_path_bytes=%d\n",
		label,
		summary.Blocks,
		summary.ColdKeys,
		summary.NodeCount,
		summary.NodeBytes,
		summary.PathCount,
		summary.PathBytesSum,
		avgPathBytes,
	)
}

func writeCSV(writer *csv.Writer, row []string) {
	if err := writer.Write(row); err != nil {
		fatalf("write csv: %v", err)
	}
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "coldtriestats: "+format+"\n", args...)
	os.Exit(1)
}
