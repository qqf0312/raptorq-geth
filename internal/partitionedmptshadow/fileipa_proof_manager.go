package partitionedmptshadow

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/internal/fileipa"
	"github.com/ethereum/go-ethereum/internal/mptagg/linearrecovery"
	"github.com/ethereum/go-ethereum/internal/mptproofmsg"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
)

type StoredFileIPAManager struct {
	store *EthDBStore

	nextID uint64

	mu      sync.Mutex
	peers   map[string]mptproofmsg.Sender
	pending map[uint64]chan *mptproofmsg.FoldedFileIPAProofPacket
}

type StoredFileIPARequestResult struct {
	ID            uint64      `json:"id"`
	Peer          string      `json:"peer"`
	Root          common.Hash `json:"root"`
	PartitionID   int         `json:"partitionID"`
	AggregateID   string      `json:"aggregateID"`
	OriginalIndex int         `json:"originalIndex"`
	Files         int         `json:"files"`
	Rows          int         `json:"rows"`
	Verified      bool        `json:"verified"`
	Recovered     common.Hash `json:"recovered"`
}

func NewStoredFileIPAManager(store *EthDBStore) *StoredFileIPAManager {
	return &StoredFileIPAManager{
		store:   store,
		peers:   make(map[string]mptproofmsg.Sender),
		pending: make(map[uint64]chan *mptproofmsg.FoldedFileIPAProofPacket),
	}
}

func (m *StoredFileIPAManager) NewHandler(peer *p2p.Peer, sender mptproofmsg.Sender) mptproofmsg.Handler {
	peerID := peerLogID(peer)
	m.mu.Lock()
	m.peers[peerID] = sender
	m.mu.Unlock()
	log.Debug("Registered MPT proof peer", "peer", peerID)
	return &managedStoredFileIPAProofHandler{
		StoredFileIPAProofHandler: StoredFileIPAProofHandler{store: m.store, sender: sender},
		manager:                   m,
		peerID:                    peerID,
	}
}

func (m *StoredFileIPAManager) RequestFoldedFileIPAProof(ctx context.Context, peerID string, root common.Hash, partitionID int, aggregateID string, originalIndex int, hash common.Hash) (*StoredFileIPARequestResult, error) {
	if m == nil {
		return nil, fmt.Errorf("nil stored FileIPA manager")
	}
	peerID = normalizePeerID(peerID)
	sender, resolvedPeer, err := m.sender(peerID)
	if err != nil {
		return nil, err
	}
	files, fileSizes, refs := fileRefsForHash(partitionID, aggregateID, originalIndex, hash.Bytes())
	requestID := atomic.AddUint64(&m.nextID, 1)
	responseCh := make(chan *mptproofmsg.FoldedFileIPAProofPacket, 1)
	m.mu.Lock()
	m.pending[requestID] = responseCh
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.pending, requestID)
		m.mu.Unlock()
	}()

	req := mptproofmsg.GetFoldedFileIPAProofPacket{
		ID:        requestID,
		Root:      root,
		Files:     refs,
		CoeffSets: mptproofmsg.ScalarMatrixToWire(RecoveryCoeffSets(len(files))),
		Domain:    StoredFileIPAProofDomain,
	}
	if err := sender.SendGetFoldedFileIPAProof(req); err != nil {
		return nil, err
	}
	var packet *mptproofmsg.FoldedFileIPAProofPacket
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case packet = <-responseCh:
	}
	recovered, verified, err := VerifyAndRecoverStoredFileIPAProof(packet, len(files), fileSizes, 0, len(files))
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(recovered, hash.Bytes()) {
		return nil, fmt.Errorf("recovered hash mismatch: got %x want %x", recovered, hash.Bytes())
	}
	return &StoredFileIPARequestResult{
		ID:            requestID,
		Peer:          resolvedPeer,
		Root:          root,
		PartitionID:   partitionID,
		AggregateID:   aggregateID,
		OriginalIndex: originalIndex,
		Files:         len(files),
		Rows:          len(packet.Rows),
		Verified:      verified,
		Recovered:     common.BytesToHash(recovered),
	}, nil
}

func (m *StoredFileIPAManager) handleFoldedFileIPAProof(packet *mptproofmsg.FoldedFileIPAProofPacket) error {
	ok, err := mptproofmsg.VerifyFoldedFileIPAProofPacket(packet)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("stored FileIPA proof did not verify")
	}
	m.mu.Lock()
	responseCh := m.pending[packet.ID]
	m.mu.Unlock()
	if responseCh == nil {
		log.Debug("Verified unsolicited stored FileIPA proof", "root", packet.Root, "id", packet.ID, "files", len(packet.Files), "rows", len(packet.Rows))
		return nil
	}
	select {
	case responseCh <- packet:
	default:
	}
	return nil
}

func (m *StoredFileIPAManager) unregister(peerID string) {
	m.mu.Lock()
	delete(m.peers, peerID)
	m.mu.Unlock()
	log.Debug("Unregistered MPT proof peer", "peer", peerID)
}

func (m *StoredFileIPAManager) sender(peerID string) (mptproofmsg.Sender, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if peerID != "" {
		sender := m.peers[peerID]
		if sender == nil {
			return nil, "", fmt.Errorf("unknown MPT proof peer %q", peerID)
		}
		return sender, peerID, nil
	}
	if len(m.peers) != 1 {
		return nil, "", fmt.Errorf("peer id is required when %d MPT proof peers are connected", len(m.peers))
	}
	for id, sender := range m.peers {
		return sender, id, nil
	}
	return nil, "", fmt.Errorf("no MPT proof peers connected")
}

type managedStoredFileIPAProofHandler struct {
	StoredFileIPAProofHandler
	manager *StoredFileIPAManager
	peerID  string
}

func (h *managedStoredFileIPAProofHandler) HandleFoldedFileIPAProof(packet *mptproofmsg.FoldedFileIPAProofPacket) error {
	return h.manager.handleFoldedFileIPAProof(packet)
}

func (h *managedStoredFileIPAProofHandler) CloseMPTProofPeer() {
	h.manager.unregister(h.peerID)
}

func RecoveryCoeffSets(fileCount int) [][]fr.Element {
	rowCount := nextStoredFileIPAPowerOfTwo(fileCount)
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

func VerifyAndRecoverStoredFileIPAProof(packet *mptproofmsg.FoldedFileIPAProofPacket, fileCount int, fileSizes []int, targetStart int, targetEnd int) ([]byte, bool, error) {
	verified, err := mptproofmsg.VerifyFoldedFileIPAProofPacket(packet)
	if err != nil {
		return nil, false, err
	}
	if !verified {
		return nil, false, fmt.Errorf("stored FileIPA proof did not verify")
	}
	result, _, _, err := packet.ToFileIPAResult()
	if err != nil {
		return nil, false, err
	}
	rows := make([]linearrecovery.LinearEncodedRow, len(result.Rows))
	for i := range result.Rows {
		rows[i] = linearrecovery.LinearEncodedRow{
			A: append([]fr.Element(nil), result.Rows[i].A...),
			C: result.Rows[i].C,
		}
	}
	recovered := make([]byte, 0, common.HashLength)
	for chunkIndex := targetStart; chunkIndex < targetEnd; chunkIndex++ {
		value, _, err := linearrecovery.RecoverSingleNode(rows, fileCount, chunkIndex)
		if err != nil {
			return nil, false, fmt.Errorf("recover chunk %d: %w", chunkIndex, err)
		}
		chunkBytes, err := fileipa.FrChunksToBytes([]fr.Element{value}, storedFileIPAChunkSize)
		if err != nil {
			return nil, false, fmt.Errorf("chunk %d bytes: %w", chunkIndex, err)
		}
		recovered = append(recovered, chunkBytes[:fileSizes[chunkIndex]]...)
	}
	return recovered, true, nil
}

func fileRefsForHash(partitionID int, aggregateID string, originalIndex int, hash []byte) ([][]byte, []int, []mptproofmsg.FileRefWire) {
	files := make([][]byte, 0, (len(hash)+storedFileIPAChunkSize-1)/storedFileIPAChunkSize)
	fileSizes := make([]int, 0, cap(files))
	refs := make([]mptproofmsg.FileRefWire, 0, cap(files))
	for offset := 0; offset < len(hash); offset += storedFileIPAChunkSize {
		end := offset + storedFileIPAChunkSize
		if end > len(hash) {
			end = len(hash)
		}
		chunk := make([]byte, storedFileIPAChunkSize)
		copy(chunk, hash[offset:end])
		chunkIndex := offset / storedFileIPAChunkSize
		originalSize := end - offset
		files = append(files, chunk)
		fileSizes = append(fileSizes, originalSize)
		refs = append(refs, NewStoredFileIPARef(partitionID, aggregateID, originalIndex, chunkIndex, hash, originalSize))
	}
	return files, fileSizes, refs
}

func normalizePeerID(peerID string) string {
	return strings.TrimPrefix(strings.TrimSpace(peerID), "0x")
}

func peerLogID(peer *p2p.Peer) string {
	if peer == nil {
		return ""
	}
	return peer.ID().String()
}
