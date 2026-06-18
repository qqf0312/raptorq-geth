package partitionedmptshadow

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/internal/fileipa"
	"github.com/ethereum/go-ethereum/internal/ipa"
	"github.com/ethereum/go-ethereum/internal/mptagg/linearrecovery"
	"github.com/ethereum/go-ethereum/internal/mptproofmsg"
	"github.com/ethereum/go-ethereum/internal/mptproofp2p"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestStoredShadowDBFoldedFileIPARoundTrip(t *testing.T) {
	senderPath := os.Getenv("PARTITION_SHADOW_SENDER_CHAINDATA")
	receiverPath := os.Getenv("PARTITION_SHADOW_RECEIVER_CHAINDATA")
	if senderPath == "" || receiverPath == "" {
		t.Skip("set PARTITION_SHADOW_SENDER_CHAINDATA and PARTITION_SHADOW_RECEIVER_CHAINDATA to run stored shadow DB FileIPA round trip")
	}

	senderDB := openShadowTestDB(t, senderPath)
	defer senderDB.Close()
	receiverDB := openShadowTestDB(t, receiverPath)
	defer receiverDB.Close()

	senderStore := NewEthDBStore(senderDB)
	receiverStore := NewEthDBStore(receiverDB)

	root := storedShadowTestRoot(t, senderStore)
	candidate := selectStoredFileIPACandidate(t, senderStore, receiverStore, root)
	files, fileSizes, refs, targetStart, targetEnd := chunkStoredNodeHashes(root, candidate)
	coeffSets := storedRecoveryCoeffSets(len(files))

	folded, err := fileipa.BuildFoldedFileIPA(files, coeffSets, StoredFileIPAProofDomain)
	if err != nil {
		t.Fatalf("BuildFoldedFileIPA: %v", err)
	}
	sharedQ, err := storedSharedFileIPACommitment(folded.Params, files)
	if err != nil {
		t.Fatalf("storedSharedFileIPACommitment: %v", err)
	}
	packet, err := mptproofmsg.NewFoldedFileIPAProofPacket(1, root, refs, sharedQ, StoredFileIPAProofDomain, folded)
	if err != nil {
		t.Fatalf("NewFoldedFileIPAProofPacket: %v", err)
	}

	senderTransport, receiverTransport, closeTransport := mptproofmsg.NewMemoryTransportPair(1)
	defer closeTransport()
	sender := mptproofmsg.NewSender(senderTransport)
	receiver := mptproofmsg.NewReceiver(receiverTransport)
	if err := sender.SendFoldedFileIPAProof(*packet); err != nil {
		t.Fatalf("SendFoldedFileIPAProof: %v", err)
	}
	received, err := receiver.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	proofPacket, ok := received.(*mptproofmsg.FoldedFileIPAProofPacket)
	if !ok {
		t.Fatalf("received packet type %T, want *FoldedFileIPAProofPacket", received)
	}
	recovered := verifyAndRecoverStoredProof(t, proofPacket, len(files), fileSizes, targetStart, targetEnd, candidate.Hash)

	t.Logf("stored shadow FileIPA round trip root=%s senderPartition=%d receiverPartition=%d agg=%s originalIndex=%d files=%d rows=%d recovered=%x",
		root, candidate.SenderPartition, candidate.ReceiverPartition, candidate.AggregatedNodeID, candidate.OriginalIndex, len(files), len(coeffSets), recovered)
}

func TestStoredShadowDBFoldedFileIPAP2PRoundTrip(t *testing.T) {
	senderPath := os.Getenv("PARTITION_SHADOW_SENDER_CHAINDATA")
	receiverPath := os.Getenv("PARTITION_SHADOW_RECEIVER_CHAINDATA")
	if senderPath == "" || receiverPath == "" {
		t.Skip("set PARTITION_SHADOW_SENDER_CHAINDATA and PARTITION_SHADOW_RECEIVER_CHAINDATA to run stored shadow DB FileIPA p2p round trip")
	}

	senderDB := openShadowTestDB(t, senderPath)
	defer senderDB.Close()
	receiverDB := openShadowTestDB(t, receiverPath)
	defer receiverDB.Close()

	senderStore := NewEthDBStore(senderDB)
	receiverStore := NewEthDBStore(receiverDB)

	root := storedShadowTestRoot(t, senderStore)
	candidate := selectStoredFileIPACandidate(t, senderStore, receiverStore, root)
	files, fileSizes, refs, targetStart, targetEnd := chunkStoredNodeHashes(root, candidate)
	request := mptproofmsg.GetFoldedFileIPAProofPacket{
		ID:        2,
		Root:      root,
		Files:     refs,
		CoeffSets: mptproofmsg.ScalarMatrixToWire(storedRecoveryCoeffSets(len(files))),
		Domain:    StoredFileIPAProofDomain,
	}

	leftRW, rightRW := p2p.MsgPipe()
	defer leftRW.Close()
	defer rightRW.Close()

	errc := make(chan error, 1)
	go func() {
		errc <- mptproofp2p.RunPeer(nil, leftRW, func(_ *p2p.Peer, sender mptproofmsg.Sender) mptproofmsg.Handler {
			return NewStoredFileIPAProofHandler(senderStore, sender)
		})
	}()

	receiverTransport := mptproofp2p.NewTransport(rightRW)
	if err := mptproofmsg.NewSender(receiverTransport).SendGetFoldedFileIPAProof(request); err != nil {
		t.Fatalf("SendGetFoldedFileIPAProof: %v", err)
	}
	received, err := mptproofmsg.NewReceiver(receiverTransport).Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	proofPacket, ok := received.(*mptproofmsg.FoldedFileIPAProofPacket)
	if !ok {
		t.Fatalf("received packet type %T, want *FoldedFileIPAProofPacket", received)
	}
	if proofPacket.ID != request.ID || proofPacket.Root != request.Root {
		t.Fatalf("proof metadata mismatch: got id=%d root=%s", proofPacket.ID, proofPacket.Root)
	}
	recovered := verifyAndRecoverStoredProof(t, proofPacket, len(files), fileSizes, targetStart, targetEnd, candidate.Hash)

	leftRW.Close()
	rightRW.Close()
	if err := <-errc; err != nil && err != p2p.ErrPipeClosed {
		t.Fatalf("RunPeer: %v", err)
	}

	t.Logf("stored shadow FileIPA p2p round trip root=%s senderPartition=%d receiverPartition=%d agg=%s originalIndex=%d files=%d rows=%d recovered=%x",
		root, candidate.SenderPartition, candidate.ReceiverPartition, candidate.AggregatedNodeID, candidate.OriginalIndex, len(files), len(proofPacket.Rows), recovered)
}

func verifyAndRecoverStoredProof(t *testing.T, proofPacket *mptproofmsg.FoldedFileIPAProofPacket, fileCount int, fileSizes []int, targetStart int, targetEnd int, wantHash []byte) []byte {
	t.Helper()
	verified, err := mptproofmsg.VerifyFoldedFileIPAProofPacket(proofPacket)
	if err != nil {
		t.Fatalf("VerifyFoldedFileIPAProofPacket: %v", err)
	}
	if !verified {
		t.Fatal("received folded FileIPA proof did not verify")
	}

	receivedResult, _, _, err := proofPacket.ToFileIPAResult()
	if err != nil {
		t.Fatalf("ToFileIPAResult: %v", err)
	}
	rows := storedRecoveryRowsFromFileIPARows(receivedResult.Rows)
	recovered := make([]byte, 0, len(wantHash))
	for chunkIndex := targetStart; chunkIndex < targetEnd; chunkIndex++ {
		value, _, err := linearrecovery.RecoverSingleNode(rows, fileCount, chunkIndex)
		if err != nil {
			t.Fatalf("RecoverSingleNode chunk %d: %v", chunkIndex, err)
		}
		chunkBytes, err := fileipa.FrChunksToBytes([]fr.Element{value}, 31)
		if err != nil {
			t.Fatalf("FrChunksToBytes chunk %d: %v", chunkIndex, err)
		}
		recovered = append(recovered, chunkBytes[:fileSizes[chunkIndex]]...)
	}
	if !bytes.Equal(recovered, wantHash) {
		t.Fatalf("recovered hash mismatch: got %x want %x", recovered, wantHash)
	}
	return recovered
}

type storedFileIPACandidate struct {
	SenderPartition   int
	ReceiverPartition int
	AggregatedNodeID  string
	OriginalIndex     int
	Hash              []byte
	AllHashes         [][]byte
}

func openShadowTestDB(t *testing.T, path string) ethdb.Database {
	t.Helper()
	db, err := rawdb.Open(rawdb.OpenOptions{
		Directory:         path,
		AncientsDirectory: filepath.Join(path, "ancient"),
		Namespace:         "eth/db/chaindata/",
		Cache:             16,
		Handles:           16,
		ReadOnly:          true,
	})
	if err != nil {
		t.Fatalf("open chaindata %s: %v", path, err)
	}
	return db
}

func storedShadowTestRoot(t *testing.T, store *EthDBStore) common.Hash {
	t.Helper()
	if rootText := os.Getenv("PARTITION_SHADOW_ROOT"); rootText != "" {
		return common.HexToHash(rootText)
	}
	results := storedResults(t, store)
	if len(results) == 0 {
		t.Fatal("no stored partition-shadow/result entries")
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Block < results[j].Block
	})
	return results[len(results)-1].Root
}

func storedResults(t *testing.T, store *EthDBStore) []*StoredResult {
	t.Helper()
	it := store.db.NewIterator(shadowResultPrefix, nil)
	defer it.Release()
	var out []*StoredResult
	for it.Next() {
		var result StoredResult
		if err := rlp.DecodeBytes(it.Value(), &result); err != nil {
			t.Fatalf("decode stored result %x: %v", it.Key(), err)
		}
		out = append(out, &result)
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterate stored results: %v", err)
	}
	return out
}

func selectStoredFileIPACandidate(t *testing.T, senderStore *EthDBStore, receiverStore *EthDBStore, root common.Hash) storedFileIPACandidate {
	t.Helper()
	result, ok, err := senderStore.StoredResult(root)
	if err != nil {
		t.Fatalf("sender StoredResult: %v", err)
	}
	if !ok {
		t.Fatalf("sender missing stored result for root %s", root)
	}
	partitions := storedPartitionIDs(result)
	for _, senderPartition := range partitions {
		view, ok, err := senderStore.StoredLocalView(root, senderPartition)
		if err != nil {
			t.Fatalf("sender StoredLocalView partition-%d: %v", senderPartition, err)
		}
		if !ok {
			continue
		}
		for _, receiverPartition := range receiverPartitionOrder(partitions, senderPartition) {
			raw, ok, err := receiverStore.StoredRawSet(root, receiverPartition)
			if err != nil {
				t.Fatalf("receiver StoredRawSet partition-%d: %v", receiverPartition, err)
			}
			if !ok {
				continue
			}
			rawHashes := make(map[string]bool, len(raw.Hashes))
			for _, hash := range raw.Hashes {
				rawHashes[normalizeStoreRawHashKey(hash)] = true
			}
			for _, agg := range view.AggregatedNodes {
				if len(agg.OriginalNodeHashes) == 0 {
					continue
				}
				for index, hash := range agg.OriginalNodeHashes {
					if len(hash) == 0 {
						continue
					}
					if rawHashes[common.Bytes2Hex(hash)] {
						continue
					}
					return storedFileIPACandidate{
						SenderPartition:   senderPartition,
						ReceiverPartition: receiverPartition,
						AggregatedNodeID:  agg.ID,
						OriginalIndex:     index,
						Hash:              append([]byte(nil), hash...),
						AllHashes:         cloneBytes2D(agg.OriginalNodeHashes),
					}
				}
			}
		}
	}
	t.Fatalf("no sender aggregated node hash missing from receiver raw set for root %s", root)
	return storedFileIPACandidate{}
}

func receiverPartitionOrder(partitions []int, senderPartition int) []int {
	out := make([]int, 0, len(partitions))
	for _, partition := range partitions {
		if partition != senderPartition {
			out = append(out, partition)
		}
	}
	out = append(out, senderPartition)
	return out
}

func storedPartitionIDs(result *StoredResult) []int {
	seen := make(map[int]bool, len(result.PartitionIDs))
	var out []int
	for _, id := range result.PartitionIDs {
		value := int(id)
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Ints(out)
	return out
}

func chunkStoredNodeHashes(root common.Hash, candidate storedFileIPACandidate) ([][]byte, []int, []mptproofmsg.FileRefWire, int, int) {
	var files [][]byte
	var fileSizes []int
	var refs []mptproofmsg.FileRefWire
	targetStart := -1
	targetEnd := -1
	for hashIndex, hash := range candidate.AllHashes {
		if hashIndex == candidate.OriginalIndex {
			targetStart = len(files)
		}
		for offset := 0; offset < len(hash); offset += 31 {
			end := offset + 31
			if end > len(hash) {
				end = len(hash)
			}
			chunk := make([]byte, 31)
			copy(chunk, hash[offset:end])
			originalSize := end - offset
			files = append(files, chunk)
			fileSizes = append(fileSizes, originalSize)
			refs = append(refs, NewStoredFileIPARef(candidate.SenderPartition, candidate.AggregatedNodeID, hashIndex, offset/31, hash, originalSize))
		}
		if hashIndex == candidate.OriginalIndex {
			targetEnd = len(files)
		}
	}
	return files, fileSizes, refs, targetStart, targetEnd
}

func storedRecoveryCoeffSets(fileCount int) [][]fr.Element {
	rowCount := nextStoredPowerOfTwo(fileCount)
	rows := make([][]fr.Element, rowCount)
	for row := range rows {
		rows[row] = make([]fr.Element, fileCount)
		var x fr.Element
		x.SetUint64(uint64(row + 1))
		var power fr.Element
		power.SetOne()
		for col := 0; col < fileCount; col++ {
			rows[row][col] = power
			power.Mul(&power, &x)
		}
	}
	return rows
}

func storedSharedFileIPACommitment(params *ipa.Params, files [][]byte) (bn254.G1Affine, error) {
	bFlat := make([]fr.Element, 0, len(files))
	for i := range files {
		chunks, err := fileipa.BytesToFrChunks(files[i])
		if err != nil {
			return bn254.G1Affine{}, fmt.Errorf("file %d chunks: %w", i, err)
		}
		bFlat = append(bFlat, chunks...)
	}
	padded := make([]fr.Element, nextStoredPowerOfTwo(len(bFlat)))
	copy(padded, bFlat)
	return ipa.CommitB(params, padded)
}

func storedRecoveryRowsFromFileIPARows(rows []fileipa.FileIPARow) []linearrecovery.LinearEncodedRow {
	out := make([]linearrecovery.LinearEncodedRow, len(rows))
	for i := range rows {
		out[i] = linearrecovery.LinearEncodedRow{
			A: append([]fr.Element(nil), rows[i].A...),
			C: rows[i].C,
		}
	}
	return out
}

func nextStoredPowerOfTwo(n int) int {
	if n <= 1 {
		return 1
	}
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}
