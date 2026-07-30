package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/internal/fountainmptshadow"
	"github.com/ethereum/go-ethereum/log"
)

func main() {
	var (
		nodeCount = flag.Uint64("nodes", 4, "number of fountain storage nodes")
		seed      = flag.String("seed", "fountainmptshadow/geth/v1", "shared key-owner seed")
	)
	flag.Parse()
	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(io.Discard, log.LevelCrit, false)))
	if *nodeCount == 0 {
		fatalf("nodes must be greater than zero")
	}

	seenPaths := make(map[string]int)
	seenHistory := make(map[string]int)
	totalInvalid := 0
	totalMisplaced := 0
	totalAggregateProofs := 0
	var baseline *fountainmptshadow.NodeUpdateStats
	var cluster fountainmptshadow.StorageBreakdown
	var physicalBytes uint64
	for index := uint64(0); index < *nodeCount; index++ {
		datadir := fmt.Sprintf("data/node%d", index+1)
		path := filepath.Join(datadir, "geth", "fountainmptdata")
		db, err := rawdb.Open(rawdb.OpenOptions{
			Directory: path,
			Cache:     16,
			Handles:   16,
			ReadOnly:  true,
		})
		if err != nil {
			fatalf("open node%d database: %v", index, err)
		}
		store := fountainmptshadow.NewEthDBStore(db)
		summary, err := store.Audit()
		if err != nil {
			db.Close()
			fatalf("audit node%d: %v", index, err)
		}
		sizes, err := store.StorageBreakdown()
		db.Close()
		if err != nil {
			fatalf("measure node%d: %v", index, err)
		}
		if baseline == nil {
			copy := sizes.Baseline
			baseline = &copy
		} else if *baseline != sizes.Baseline {
			fatalf("node%d baseline differs: node0=%+v node%d=%+v", index, *baseline, index, sizes.Baseline)
		}
		addStorageBreakdown(&cluster, sizes)
		totalAggregateProofs += summary.AggregateProofs
		diskBytes, err := directoryBytes(path)
		if err != nil {
			fatalf("measure node%d directory: %v", index, err)
		}
		physicalBytes += diskBytes
		fmt.Printf("node%d roots=%d versions=%d latestPaths=%d historicalLeaves=%d aggregateProofs=%d\n",
			index,
			summary.Roots,
			summary.Versions,
			len(summary.Paths),
			len(summary.HistoricalLeaves),
			summary.AggregateProofs,
		)
		for _, path := range summary.Paths {
			owner := fountainmptshadow.KeyOwner([]byte(*seed), path.Key, *nodeCount)
			status := "PASS"
			if !path.Verified {
				status = "INVALID"
				totalInvalid++
			}
			if owner != index {
				status = "MISPLACED"
				totalMisplaced++
			}
			id := fmt.Sprintf("%x/%x", path.Root, path.Key)
			seenPaths[id]++
			fmt.Printf("  latest height=%d key=%x owner=node%d rows=%d proof=%s\n",
				path.Height,
				path.Key,
				owner,
				path.Rows,
				status,
			)
		}
		for _, history := range summary.HistoricalLeaves {
			owner := fountainmptshadow.KeyOwner([]byte(*seed), history.Key, *nodeCount)
			status := "PASS"
			if !history.Verified {
				status = "INVALID"
				totalInvalid++
			}
			if owner != index {
				status = "MISPLACED"
				totalMisplaced++
			}
			id := fmt.Sprintf("%x/%x", history.TerminalRoot, history.Key)
			seenHistory[id]++
			fmt.Printf("  history version=[%d,%d] key=%x owner=node%d aggregateProof=%s\n",
				history.StartHeight,
				history.TerminalHeight,
				history.Key,
				owner,
				status,
			)
		}
	}
	duplicates := duplicateCount(seenPaths) + duplicateCount(seenHistory)
	fmt.Printf("summary misplaced=%d duplicateRecords=%d invalidProofs=%d aggregateProofs=%d\n",
		totalMisplaced, duplicates, totalInvalid, totalAggregateProofs)
	if baseline != nil {
		oldBytes := baseline.BlobBytes + baseline.HashBytes
		newCoreBytes := cluster.EncodedBlocksBytes +
			cluster.HistoricalLeafBytes +
			cluster.GenerationMatrixBytes +
			cluster.CommitmentBytes +
			cluster.ProofBytes
		fmt.Printf("\nstorage comparison (all %d nodes combined; baseline counted once)\n", *nodeCount)
		fmt.Printf("  original MPT updatedNodes=%d deletes=%d blobs=%s hashKeys=%s total=%s\n",
			baseline.Nodes, baseline.Deletes, byteSize(baseline.BlobBytes), byteSize(baseline.HashBytes), byteSize(oldBytes))
		fmt.Printf("  fountain encodedBlocks=%s historicalLeaves=%s matrices=%s commitments=%s proofs=%s coreTotal=%s\n",
			byteSize(cluster.EncodedBlocksBytes),
			byteSize(cluster.HistoricalLeafBytes),
			byteSize(cluster.GenerationMatrixBytes),
			byteSize(cluster.CommitmentBytes),
			byteSize(cluster.ProofBytes),
			byteSize(newCoreBytes),
		)
		fmt.Printf("    proof detail: rowStatements=%s foldChallenges=%s ipaProofs=%s\n",
			byteSize(cluster.ProofStatementBytes),
			byteSize(cluster.ProofFoldChallengeBytes),
			byteSize(cluster.ProofIPABytes),
		)
		fmt.Printf("  fountain roots=%s versionIndexes=%s otherMetadata=%s exactKV=%s physicalLevelDB=%s\n",
			byteSize(cluster.RootRecordBytes),
			byteSize(cluster.VersionIndexBytes),
			byteSize(cluster.OtherRecordMetadataBytes),
			byteSize(cluster.RecordBytes),
			byteSize(physicalBytes),
		)
		if oldBytes != 0 {
			fmt.Printf("  ratio vs one original copy: core=%.3fx exactKV=%.3fx\n",
				float64(newCoreBytes)/float64(oldBytes),
				float64(cluster.RecordBytes)/float64(oldBytes),
			)
			replicatedOldBytes := oldBytes * *nodeCount
			fmt.Printf("  ratio vs %d fully replicated original copies (%s): core=%.3fx exactKV=%.3fx\n",
				*nodeCount,
				byteSize(replicatedOldBytes),
				float64(newCoreBytes)/float64(replicatedOldBytes),
				float64(cluster.RecordBytes)/float64(replicatedOldBytes),
			)
		}
	}
	if totalMisplaced != 0 || duplicates != 0 || totalInvalid != 0 {
		os.Exit(1)
	}
}

func addStorageBreakdown(total *fountainmptshadow.StorageBreakdown, next *fountainmptshadow.StorageBreakdown) {
	total.EncodedBlocksBytes += next.EncodedBlocksBytes
	total.HistoricalLeafBytes += next.HistoricalLeafBytes
	total.GenerationMatrixBytes += next.GenerationMatrixBytes
	total.CommitmentBytes += next.CommitmentBytes
	total.ProofBytes += next.ProofBytes
	total.ProofStatementBytes += next.ProofStatementBytes
	total.ProofFoldChallengeBytes += next.ProofFoldChallengeBytes
	total.ProofIPABytes += next.ProofIPABytes
	total.RootRecordBytes += next.RootRecordBytes
	total.VersionIndexBytes += next.VersionIndexBytes
	total.OtherRecordMetadataBytes += next.OtherRecordMetadataBytes
	total.RecordBytes += next.RecordBytes
}

func directoryBytes(root string) (uint64, error) {
	var total uint64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += uint64(info.Size())
		}
		return nil
	})
	return total, err
}

func byteSize(bytes uint64) string {
	return fmt.Sprintf("%d B (%.2f KiB)", bytes, float64(bytes)/1024)
}

func duplicateCount(records map[string]int) int {
	count := 0
	for _, copies := range records {
		if copies > 1 {
			count += copies - 1
		}
	}
	return count
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
