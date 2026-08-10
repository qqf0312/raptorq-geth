package fountainmptshadow

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/internal/ipa"
	"github.com/ethereum/go-ethereum/internal/mptagg/linearrecovery"
	"github.com/ethereum/go-ethereum/internal/mptproofmsg"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
)

type recoveryAggregateKey struct {
	height uint64
	id     common.Hash
}

// RecoveryManager implements the two-stage recovery protocol. The expected Q
// is supplied by the caller from its local path-commitment store and is never
// learned from, or overwritten by, the remote response.
type RecoveryManager struct {
	store *EthDBStore

	nextID   uint64
	mu       sync.Mutex
	peers    map[string]mptproofmsg.FountainSender
	offers   map[uint64]chan *mptproofmsg.FountainOfferPacket
	bundles  map[uint64]chan *mptproofmsg.FountainAggregatePacket
	verified map[recoveryAggregateKey]*EpochAggregate
}

type RecoveryResult struct {
	ID              uint64      `json:"id"`
	Peer            string      `json:"peer"`
	Root            common.Hash `json:"root"`
	Key             []byte      `json:"key"`
	AggregateID     common.Hash `json:"aggregateID"`
	SourceCount     uint64      `json:"sourceCount"`
	RowCount        uint64      `json:"rowCount"`
	OfferBytes      uint64      `json:"offerBytes"`
	AggregateBytes  uint64      `json:"aggregateBytes"`
	AggregateCached bool        `json:"aggregateCached"`
	IPAValidated    bool        `json:"ipaValidated"`
	Nodes           [][]byte    `json:"nodes"`
}

func NewRecoveryManager(store *EthDBStore) *RecoveryManager {
	return &RecoveryManager{
		store:    store,
		peers:    make(map[string]mptproofmsg.FountainSender),
		offers:   make(map[uint64]chan *mptproofmsg.FountainOfferPacket),
		bundles:  make(map[uint64]chan *mptproofmsg.FountainAggregatePacket),
		verified: make(map[recoveryAggregateKey]*EpochAggregate),
	}
}

func (m *RecoveryManager) NewHandler(peer *p2p.Peer, sender mptproofmsg.Sender) mptproofmsg.FountainHandler {
	peerID := fountainPeerID(peer)
	fountainSender, _ := sender.(mptproofmsg.FountainSender)
	m.mu.Lock()
	if fountainSender != nil {
		m.peers[peerID] = fountainSender
	}
	m.mu.Unlock()
	return &managedRecoveryHandler{manager: m, peerID: peerID, sender: fountainSender}
}

// RecoverRemotePath performs offer/rank-check, aggregate IPA verification,
// local-Q binding and linear path recovery, in that order.
func (m *RecoveryManager) RecoverRemotePath(ctx context.Context, peerID string, root common.Hash, key []byte, expectedQ bn254.G1Affine) (*RecoveryResult, error) {
	return m.recoverRemotePath(ctx, peerID, root, key, func(*mptproofmsg.FountainOfferPacket) (bn254.G1Affine, error) {
		return expectedQ, nil
	})
}

// RecoverRemotePathWithResolver lets a live Geth node derive the trusted Q
// from its own retained MPT after seeing the aggregate vector layout in the
// offer. The resolver must never accept Q from the serving peer.
func (m *RecoveryManager) RecoverRemotePathWithResolver(ctx context.Context, peerID string, root common.Hash, key []byte, resolve func(*mptproofmsg.FountainOfferPacket) (bn254.G1Affine, error)) (*RecoveryResult, error) {
	if resolve == nil {
		return nil, fmt.Errorf("nil local commitment resolver")
	}
	return m.recoverRemotePath(ctx, peerID, root, key, resolve)
}

func (m *RecoveryManager) recoverRemotePath(ctx context.Context, peerID string, root common.Hash, key []byte, resolve func(*mptproofmsg.FountainOfferPacket) (bn254.G1Affine, error)) (*RecoveryResult, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("nil fountain recovery manager or store")
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("empty recovery key")
	}
	sender, resolvedPeer, err := m.sender(peerID)
	if err != nil {
		return nil, err
	}
	offerID := atomic.AddUint64(&m.nextID, 1)
	offerCh := make(chan *mptproofmsg.FountainOfferPacket, 1)
	m.mu.Lock()
	m.offers[offerID] = offerCh
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.offers, offerID)
		m.mu.Unlock()
	}()
	if err := sender.SendGetFountainOffer(mptproofmsg.GetFountainOfferPacket{ID: offerID, Root: root, PathKey: append([]byte(nil), key...)}); err != nil {
		return nil, err
	}
	var offer *mptproofmsg.FountainOfferPacket
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case offer = <-offerCh:
	}
	if offer == nil || !offer.Found {
		return nil, fmt.Errorf("peer %s has no fountain path for root=%s key=%x", resolvedPeer, root, key)
	}
	if offer.SourceCount == 0 || offer.RowCount == 0 || offer.NodeCount == 0 || len(offer.Seed) == 0 ||
		offer.WitnessLength == 0 || offer.VectorLength == 0 || offer.SegmentOffset > offer.VectorLength ||
		offer.WitnessLength > offer.VectorLength-offer.SegmentOffset {
		return nil, fmt.Errorf("invalid fountain offer")
	}
	matrix, err := GenerateMatrix(offer.Seed, int(offer.SourceCount), int(offer.RowCount))
	if err != nil {
		return nil, err
	}
	if uint64(len(matrix)) != offer.RowCount {
		return nil, fmt.Errorf("offer row count %d regenerates %d rows", offer.RowCount, len(matrix))
	}
	if err := requireFullRecovery(matrix); err != nil {
		return nil, fmt.Errorf("offered seed is not sufficient for full path recovery: %w", err)
	}
	expectedQ, err := resolve(offer)
	if err != nil {
		return nil, fmt.Errorf("resolve local path commitment: %w", err)
	}

	cacheKey := recoveryAggregateKey{height: offer.AggregateHeight, id: offer.AggregateID}
	m.mu.Lock()
	aggregate := m.verified[cacheKey]
	m.mu.Unlock()
	offerPayload, _ := rlp.EncodeToBytes(offer)
	result := &RecoveryResult{
		ID: offerID, Peer: resolvedPeer, Root: root, Key: append([]byte(nil), key...),
		AggregateID: offer.AggregateID, SourceCount: offer.SourceCount, RowCount: offer.RowCount,
		OfferBytes: uint64(len(offerPayload)), AggregateCached: aggregate != nil,
	}
	if aggregate == nil {
		bundleID := atomic.AddUint64(&m.nextID, 1)
		bundleCh := make(chan *mptproofmsg.FountainAggregatePacket, 1)
		m.mu.Lock()
		m.bundles[bundleID] = bundleCh
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			delete(m.bundles, bundleID)
			m.mu.Unlock()
		}()
		if err := sender.SendGetFountainAggregate(mptproofmsg.GetFountainAggregatePacket{
			ID: bundleID, Height: offer.AggregateHeight, AggregateID: offer.AggregateID,
		}); err != nil {
			return nil, err
		}
		var packet *mptproofmsg.FountainAggregatePacket
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case packet = <-bundleCh:
		}
		if packet == nil || !packet.Found {
			return nil, fmt.Errorf("peer %s has no fountain aggregate %s", resolvedPeer, offer.AggregateID)
		}
		if packet.Height != offer.AggregateHeight || packet.AggregateID != offer.AggregateID {
			return nil, fmt.Errorf("fountain aggregate response does not match offer")
		}
		aggregate, err = DecodeRecoveryAggregate(packet.Bundle)
		if err != nil {
			return nil, fmt.Errorf("decode fountain aggregate: %w", err)
		}
		packetPayload, _ := rlp.EncodeToBytes(packet)
		result.AggregateBytes = uint64(len(packetPayload))
		if epochAggregateID(aggregate) != offer.AggregateID {
			return nil, fmt.Errorf("fountain aggregate content ID mismatch")
		}
	}
	encoded, err := EncodedPathFromAggregate(root, key, aggregate)
	if err != nil {
		return nil, err
	}
	if !encoded.Q.Equal(&expectedQ) {
		return nil, fmt.Errorf("aggregate path commitment does not match local Q")
	}
	verified, err := VerifyEpochAggregate(aggregate)
	if err != nil {
		return nil, fmt.Errorf("verify fountain aggregate IPA: %w", err)
	}
	if !verified {
		return nil, fmt.Errorf("fountain aggregate IPA proof did not verify")
	}
	if !bytes.Equal(encoded.Seed, offer.Seed) || encoded.Layout.SourceCount != offer.SourceCount ||
		uint64(len(encoded.Matrix)) != offer.RowCount || encoded.Layout.NodeCount != offer.NodeCount ||
		encoded.Layout.CompactBytes != offer.CompactBytes {
		return nil, fmt.Errorf("verified aggregate path does not match offer")
	}
	targetRelation := aggregate.Relations[encoded.AggregateStart]
	if targetRelation.Offset != offer.SegmentOffset || targetRelation.Length != offer.WitnessLength ||
		len(aggregate.Proof.Rows) == 0 || uint64(len(aggregate.Proof.Rows[0].A)) != offer.VectorLength {
		return nil, fmt.Errorf("verified aggregate vector layout does not match offer")
	}
	m.mu.Lock()
	m.verified[cacheKey] = aggregate
	m.mu.Unlock()
	nodes, err := RecoverPath(encoded)
	if err != nil {
		return nil, err
	}
	result.IPAValidated = true
	result.Nodes = nodes
	return result, nil
}

// CommitPathAtAggregateLayout computes exactly the segment Q used by the epoch
// aggregate, but from locally resolved path bytes. This is the live-node trust
// anchor used when the path has not yet been pruned.
func CommitPathAtAggregateLayout(nodes [][]byte, offset, witnessLength, vectorLength uint64) (bn254.G1Affine, error) {
	files, _, err := FramePathNodes(nodes)
	if err != nil {
		return bn254.G1Affine{}, err
	}
	witness, err := semanticWitness(files)
	if err != nil {
		return bn254.G1Affine{}, err
	}
	if uint64(len(witness)) != witnessLength || vectorLength == 0 || offset > vectorLength || witnessLength > vectorLength-offset {
		return bn254.G1Affine{}, fmt.Errorf("local path witness layout mismatch")
	}
	globalSegment := make([]fr.Element, int(vectorLength))
	copy(globalSegment[int(offset):], witness)
	params, err := ipa.NewTestParams(int(vectorLength))
	if err != nil {
		return bn254.G1Affine{}, err
	}
	return ipa.CommitB(params, globalSegment)
}

// EncodedPathFromAggregate extracts one path's verified public C matrix from
// the original epoch aggregate without changing its proof granularity.
func EncodedPathFromAggregate(root common.Hash, key []byte, aggregate *EpochAggregate) (*EncodedPath, error) {
	if aggregate == nil {
		return nil, fmt.Errorf("nil epoch aggregate")
	}
	start := -1
	for i, relation := range aggregate.Relations {
		if relation.Kind == AggregateRelationPath && relation.Root == root && bytes.Equal(relation.Key, key) && relation.Row == 0 {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("aggregate path relation not found")
	}
	first := aggregate.Relations[start]
	if first.SourceCount == 0 || first.RowCount == 0 {
		return nil, fmt.Errorf("invalid aggregate path dimensions")
	}
	chunkCount := SourceSymbolSize / SourceChunkSize
	relationCount := int(first.RowCount) * chunkCount
	if start+relationCount > len(aggregate.Relations) {
		return nil, fmt.Errorf("aggregate path relation range is out of bounds")
	}
	matrix, err := GenerateMatrix(first.Seed, int(first.SourceCount), int(first.RowCount))
	if err != nil {
		return nil, err
	}
	blocks := make([][]fr.Element, len(matrix))
	for row := range matrix {
		blocks[row] = make([]fr.Element, chunkCount)
		for chunk := 0; chunk < chunkCount; chunk++ {
			relation := aggregate.Relations[start+row*chunkCount+chunk]
			if relation.Kind != AggregateRelationPath || relation.Root != root || !bytes.Equal(relation.Key, key) ||
				relation.Row != uint64(row) || relation.Offset != first.Offset || relation.Length != first.Length ||
				relation.SourceCount != first.SourceCount || relation.RowCount != first.RowCount ||
				!bytes.Equal(relation.Seed, first.Seed) {
				return nil, fmt.Errorf("aggregate path relation %d is not canonical", row*chunkCount+chunk)
			}
			blocks[row][chunk] = relation.C
		}
	}
	q, ok := aggregateSegmentQ(aggregate, first.Offset)
	if !ok {
		return nil, fmt.Errorf("missing aggregate path segment commitment")
	}
	path := &EncodedPath{
		Height: aggregate.Height, Root: root, Key: append([]byte(nil), key...),
		Layout: PathLayout{NodeCount: first.NodeCount, SourceCount: first.SourceCount, FileSize: SourceSymbolSize, CompactBytes: first.CompactBytes},
		Seed:   append([]byte(nil), first.Seed...), Matrix: matrix, Blocks: blocks, Q: q,
		Aggregate: aggregate, AggregateStart: uint64(start),
	}
	if err := validateEncodedPath(path); err != nil {
		return nil, err
	}
	return path, nil
}

func requireFullRecovery(matrix [][]fr.Element) error {
	if len(matrix) == 0 || len(matrix[0]) == 0 {
		return fmt.Errorf("empty generation matrix")
	}
	rows := make([]linearrecovery.LinearEncodedRow, len(matrix))
	for i := range matrix {
		rows[i] = linearrecovery.LinearEncodedRow{A: matrix[i]}
	}
	for source := range matrix[0] {
		ok, err := linearrecovery.CanRecoverSingleNode(rows, len(matrix[0]), source)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("source %d is not recoverable", source)
		}
	}
	return nil
}

func (m *RecoveryManager) sender(peerID string) (mptproofmsg.FountainSender, string, error) {
	peerID = strings.TrimPrefix(strings.TrimSpace(peerID), "0x")
	m.mu.Lock()
	defer m.mu.Unlock()
	if peerID != "" {
		if sender := m.peers[peerID]; sender != nil {
			return sender, peerID, nil
		}
		return nil, "", fmt.Errorf("unknown fountain peer %q", peerID)
	}
	if len(m.peers) != 1 {
		return nil, "", fmt.Errorf("peer id is required when %d fountain peers are connected", len(m.peers))
	}
	for id, sender := range m.peers {
		return sender, id, nil
	}
	return nil, "", fmt.Errorf("no fountain peers connected")
}

type managedRecoveryHandler struct {
	manager *RecoveryManager
	peerID  string
	sender  mptproofmsg.FountainSender
}

func (h *managedRecoveryHandler) HandleGetFountainOffer(req *mptproofmsg.GetFountainOfferPacket) error {
	if h.sender == nil {
		return fmt.Errorf("fountain sender is not supported by this transport")
	}
	res := mptproofmsg.FountainOfferPacket{ID: req.ID}
	path, ok, err := h.manager.store.EncodedPath(req.Root, req.PathKey)
	if err != nil {
		return err
	}
	if ok && path.Aggregate != nil {
		res.Found = true
		res.Height = path.Height
		res.Seed = append([]byte(nil), path.Seed...)
		res.SourceCount = path.Layout.SourceCount
		res.RowCount = uint64(len(path.Matrix))
		res.NodeCount = path.Layout.NodeCount
		res.CompactBytes = path.Layout.CompactBytes
		relation := path.Aggregate.Relations[path.AggregateStart]
		res.SegmentOffset = relation.Offset
		res.WitnessLength = relation.Length
		if len(path.Aggregate.Proof.Rows) > 0 {
			res.VectorLength = uint64(len(path.Aggregate.Proof.Rows[0].A))
		}
		res.AggregateHeight = path.Aggregate.Height
		res.AggregateID = epochAggregateID(path.Aggregate)
	}
	return h.sender.SendFountainOffer(res)
}

func (h *managedRecoveryHandler) HandleFountainOffer(packet *mptproofmsg.FountainOfferPacket) error {
	h.manager.mu.Lock()
	ch := h.manager.offers[packet.ID]
	h.manager.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case ch <- packet:
	default:
	}
	return nil
}

func (h *managedRecoveryHandler) HandleGetFountainAggregate(req *mptproofmsg.GetFountainAggregatePacket) error {
	if h.sender == nil {
		return fmt.Errorf("fountain sender is not supported by this transport")
	}
	res := mptproofmsg.FountainAggregatePacket{ID: req.ID, Height: req.Height, AggregateID: req.AggregateID}
	aggregate, err := h.manager.store.EpochAggregate(req.Height, req.AggregateID)
	if err == nil {
		res.Bundle, err = EncodeRecoveryAggregate(aggregate)
		if err != nil {
			return err
		}
		res.Found = true
	}
	return h.sender.SendFountainAggregate(res)
}

func (h *managedRecoveryHandler) HandleFountainAggregate(packet *mptproofmsg.FountainAggregatePacket) error {
	h.manager.mu.Lock()
	ch := h.manager.bundles[packet.ID]
	h.manager.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case ch <- packet:
	default:
	}
	return nil
}

func (h *managedRecoveryHandler) CloseMPTProofPeer() {
	h.manager.mu.Lock()
	delete(h.manager.peers, h.peerID)
	h.manager.mu.Unlock()
	log.Debug("Unregistered fountain recovery peer", "peer", h.peerID)
}

func fountainPeerID(peer *p2p.Peer) string {
	if peer == nil {
		return ""
	}
	return peer.ID().String()
}
