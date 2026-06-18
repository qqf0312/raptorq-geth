package partitionedmptshadow

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/internal/fileipa"
	"github.com/ethereum/go-ethereum/internal/ipa"
	"github.com/ethereum/go-ethereum/internal/mptproofmsg"
	"github.com/ethereum/go-ethereum/log"
)

const (
	StoredFileIPAProofDomain = "partitionedmptshadow/stored-fileipa/recovery-v1"

	storedFileIPARefPrefix = "partition-shadow-fileipa"
	storedFileIPAChunkSize = 31
)

type StoredFileIPAProofHandler struct {
	store  *EthDBStore
	sender mptproofmsg.Sender
}

func NewStoredFileIPAProofHandler(store *EthDBStore, sender mptproofmsg.Sender) *StoredFileIPAProofHandler {
	return &StoredFileIPAProofHandler{store: store, sender: sender}
}

func (h *StoredFileIPAProofHandler) HandleGetCommitment(*mptproofmsg.GetCommitmentPacket) error {
	return nil
}

func (h *StoredFileIPAProofHandler) HandleCommitment(*mptproofmsg.CommitmentPacket) error {
	return nil
}

func (h *StoredFileIPAProofHandler) HandleGetProof(*mptproofmsg.GetProofPacket) error {
	return nil
}

func (h *StoredFileIPAProofHandler) HandleProof(*mptproofmsg.ProofPacket) error {
	return nil
}

func (h *StoredFileIPAProofHandler) HandleGetFoldedFileIPAProof(req *mptproofmsg.GetFoldedFileIPAProofPacket) error {
	if h == nil || h.sender == nil {
		return fmt.Errorf("nil stored FileIPA proof sender")
	}
	packet, err := BuildStoredFoldedFileIPAProofPacket(h.store, req)
	if err != nil {
		return err
	}
	return h.sender.SendFoldedFileIPAProof(*packet)
}

func (h *StoredFileIPAProofHandler) HandleFoldedFileIPAProof(packet *mptproofmsg.FoldedFileIPAProofPacket) error {
	ok, err := mptproofmsg.VerifyFoldedFileIPAProofPacket(packet)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("stored FileIPA proof did not verify")
	}
	log.Debug("Verified stored FileIPA proof", "root", packet.Root, "id", packet.ID, "files", len(packet.Files), "rows", len(packet.Rows))
	return nil
}

func BuildStoredFoldedFileIPAProofPacket(store *EthDBStore, req *mptproofmsg.GetFoldedFileIPAProofPacket) (*mptproofmsg.FoldedFileIPAProofPacket, error) {
	if req == nil {
		return nil, fmt.Errorf("nil folded FileIPA proof request")
	}
	if req.Domain == "" {
		return nil, fmt.Errorf("empty folded FileIPA proof domain")
	}
	files, err := ResolveStoredFileIPARefs(store, req.Root, req.Files)
	if err != nil {
		return nil, err
	}
	coeffSets, err := mptproofmsg.ScalarMatrixFromWire(req.CoeffSets)
	if err != nil {
		return nil, fmt.Errorf("coefficient sets: %w", err)
	}
	folded, err := fileipa.BuildFoldedFileIPA(files, coeffSets, req.Domain)
	if err != nil {
		return nil, fmt.Errorf("build folded FileIPA: %w", err)
	}
	sharedQ, err := storedFileIPACommitment(folded.Params, files)
	if err != nil {
		return nil, err
	}
	return mptproofmsg.NewFoldedFileIPAProofPacket(req.ID, req.Root, req.Files, sharedQ, req.Domain, folded)
}

func ResolveStoredFileIPARefs(store *EthDBStore, root common.Hash, refs []mptproofmsg.FileRefWire) ([][]byte, error) {
	if len(refs) == 0 {
		return nil, fmt.Errorf("empty stored FileIPA file refs")
	}
	files := make([][]byte, len(refs))
	for i := range refs {
		file, err := ResolveStoredFileIPARef(store, root, refs[i])
		if err != nil {
			return nil, fmt.Errorf("file ref %d: %w", i, err)
		}
		files[i] = file
	}
	return files, nil
}

func ResolveStoredFileIPARef(store *EthDBStore, root common.Hash, ref mptproofmsg.FileRefWire) ([]byte, error) {
	if store == nil {
		return nil, fmt.Errorf("nil shadow store")
	}
	loc, err := ParseStoredFileIPARefKey(ref.Key)
	if err != nil {
		return nil, err
	}
	if ref.Size == 0 || ref.Size > storedFileIPAChunkSize {
		return nil, fmt.Errorf("invalid stored FileIPA ref size %d", ref.Size)
	}
	view, ok, err := store.StoredLocalView(root, loc.PartitionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("missing local view root=%s partition=%d", root, loc.PartitionID)
	}
	for _, agg := range view.AggregatedNodes {
		if agg.ID != loc.AggregatedNodeID {
			continue
		}
		if loc.OriginalIndex < 0 || loc.OriginalIndex >= len(agg.OriginalNodeHashes) {
			return nil, fmt.Errorf("original index %d out of range for aggregate %s", loc.OriginalIndex, loc.AggregatedNodeID)
		}
		hash := agg.OriginalNodeHashes[loc.OriginalIndex]
		if ref.Hash != (common.BytesToHash(hash)) {
			return nil, fmt.Errorf("hash mismatch for aggregate %s original %d", loc.AggregatedNodeID, loc.OriginalIndex)
		}
		start := loc.ChunkIndex * storedFileIPAChunkSize
		if start >= len(hash) {
			return nil, fmt.Errorf("chunk index %d out of range for hash length %d", loc.ChunkIndex, len(hash))
		}
		end := start + storedFileIPAChunkSize
		if end > len(hash) {
			end = len(hash)
		}
		if int(ref.Size) != end-start {
			return nil, fmt.Errorf("chunk size mismatch: ref=%d stored=%d", ref.Size, end-start)
		}
		chunk := make([]byte, storedFileIPAChunkSize)
		copy(chunk, hash[start:end])
		return chunk, nil
	}
	return nil, fmt.Errorf("missing aggregate %s in root=%s partition=%d", loc.AggregatedNodeID, root, loc.PartitionID)
}

type StoredFileIPARefLocation struct {
	PartitionID      int
	AggregatedNodeID string
	OriginalIndex    int
	ChunkIndex       int
}

func NewStoredFileIPARef(partitionID int, aggregateID string, originalIndex int, chunkIndex int, hash []byte, size int) mptproofmsg.FileRefWire {
	return mptproofmsg.FileRefWire{
		Key:  StoredFileIPARefKey(partitionID, aggregateID, originalIndex, chunkIndex),
		Hash: common.BytesToHash(hash),
		Size: uint64(size),
	}
}

func StoredFileIPARefKey(partitionID int, aggregateID string, originalIndex int, chunkIndex int) []byte {
	escapedID := url.PathEscape(aggregateID)
	return []byte(fmt.Sprintf("%s/%d/%s/%d/%d", storedFileIPARefPrefix, partitionID, escapedID, originalIndex, chunkIndex))
}

func ParseStoredFileIPARefKey(key []byte) (StoredFileIPARefLocation, error) {
	parts := strings.Split(string(key), "/")
	if len(parts) != 5 || parts[0] != storedFileIPARefPrefix {
		return StoredFileIPARefLocation{}, fmt.Errorf("invalid stored FileIPA ref key %q", string(key))
	}
	partitionID, err := strconv.Atoi(parts[1])
	if err != nil {
		return StoredFileIPARefLocation{}, fmt.Errorf("invalid partition id %q: %w", parts[1], err)
	}
	aggregateID, err := url.PathUnescape(parts[2])
	if err != nil {
		return StoredFileIPARefLocation{}, fmt.Errorf("invalid aggregate id %q: %w", parts[2], err)
	}
	originalIndex, err := strconv.Atoi(parts[3])
	if err != nil {
		return StoredFileIPARefLocation{}, fmt.Errorf("invalid original index %q: %w", parts[3], err)
	}
	chunkIndex, err := strconv.Atoi(parts[4])
	if err != nil {
		return StoredFileIPARefLocation{}, fmt.Errorf("invalid chunk index %q: %w", parts[4], err)
	}
	return StoredFileIPARefLocation{
		PartitionID:      partitionID,
		AggregatedNodeID: aggregateID,
		OriginalIndex:    originalIndex,
		ChunkIndex:       chunkIndex,
	}, nil
}

func storedFileIPACommitment(params *ipa.Params, files [][]byte) (bn254.G1Affine, error) {
	bFlat := make([]fr.Element, 0, len(files))
	for i := range files {
		chunks, err := fileipa.BytesToFrChunks(files[i])
		if err != nil {
			return bn254.G1Affine{}, fmt.Errorf("file %d chunks: %w", i, err)
		}
		bFlat = append(bFlat, chunks...)
	}
	padded := make([]fr.Element, nextStoredFileIPAPowerOfTwo(len(bFlat)))
	copy(padded, bFlat)
	return ipa.CommitB(params, padded)
}

func nextStoredFileIPAPowerOfTwo(n int) int {
	if n <= 1 {
		return 1
	}
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}
