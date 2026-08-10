package fountainmptshadow

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/internal/mptproofmsg"
)

func TestRecoveryAggregateCodecRoundTrip(t *testing.T) {
	store, root, key, nodes, expectedQ := buildRecoveryStore(t, 4)
	path, ok, err := store.EncodedPath(root, key)
	if err != nil || !ok {
		t.Fatalf("EncodedPath: ok=%v err=%v", ok, err)
	}
	blob, err := EncodeRecoveryAggregate(path.Aggregate)
	if err != nil {
		t.Fatalf("EncodeRecoveryAggregate: %v", err)
	}
	decoded, err := DecodeRecoveryAggregate(blob)
	if err != nil {
		t.Fatalf("DecodeRecoveryAggregate: %v", err)
	}
	if epochAggregateID(decoded) != epochAggregateID(path.Aggregate) {
		t.Fatal("aggregate content ID changed across network codec")
	}
	ok, err = VerifyEpochAggregate(decoded)
	if err != nil || !ok {
		t.Fatalf("VerifyEpochAggregate: ok=%v err=%v", ok, err)
	}
	decodedPath, err := EncodedPathFromAggregate(root, key, decoded)
	if err != nil {
		t.Fatalf("EncodedPathFromAggregate: %v", err)
	}
	if !decodedPath.Q.Equal(&expectedQ) {
		t.Fatal("decoded path Q changed")
	}
	relation := decoded.Relations[decodedPath.AggregateStart]
	localQ, err := CommitPathAtAggregateLayout(nodes, relation.Offset, relation.Length, uint64(len(decoded.Proof.Rows[0].A)))
	if err != nil {
		t.Fatalf("CommitPathAtAggregateLayout: %v", err)
	}
	if !localQ.Equal(&expectedQ) {
		t.Fatal("locally resolved MPT path did not reproduce aggregate segment Q")
	}
}

func TestRecoveryAggregateRejectsTamperedIPAProof(t *testing.T) {
	store, root, key, _, _ := buildRecoveryStore(t, 4)
	path, ok, err := store.EncodedPath(root, key)
	if err != nil || !ok {
		t.Fatalf("EncodedPath: ok=%v err=%v", ok, err)
	}
	blob, err := EncodeRecoveryAggregate(path.Aggregate)
	if err != nil {
		t.Fatal(err)
	}
	blob[len(blob)-1] ^= 1
	aggregate, err := DecodeRecoveryAggregate(blob)
	if err != nil {
		return // Non-canonical tampering is rejected during decoding.
	}
	verified, err := VerifyEpochAggregate(aggregate)
	if err == nil && verified {
		t.Fatal("tampered aggregate IPA proof verified")
	}
}

func TestRecoveryManagerVerifiesIPAAndLocalQBeforeRecovering(t *testing.T) {
	serverStore, root, key, wantNodes, expectedQ := buildRecoveryStore(t, 4)
	clientStore := NewEthDBStore(rawdb.NewMemoryDatabase())
	server := NewRecoveryManager(serverStore)
	client := NewRecoveryManager(clientStore)
	closeNetwork := connectRecoveryManagers(t, server, client)
	defer closeNetwork()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.RecoverRemotePath(ctx, "", root, key, expectedQ)
	if err != nil {
		t.Fatalf("RecoverRemotePath: %v", err)
	}
	if !result.IPAValidated || result.AggregateCached {
		t.Fatalf("unexpected verification result: %+v", result)
	}
	if !nodesEqual(result.Nodes, wantNodes) {
		t.Fatal("recovered path mismatch")
	}
	if result.AggregateBytes == 0 || result.OfferBytes == 0 {
		t.Fatalf("missing communication accounting: %+v", result)
	}

	// The second request reuses the already verified node/epoch aggregate but
	// still rechecks that this target segment matches the caller's local Q.
	result, err = client.RecoverRemotePath(ctx, "", root, key, expectedQ)
	if err != nil {
		t.Fatalf("RecoverRemotePath cached: %v", err)
	}
	if !result.AggregateCached || result.AggregateBytes != 0 {
		t.Fatalf("aggregate was not reused: %+v", result)
	}

	wrongQ := expectedQ
	wrongQ.Y.Neg(&wrongQ.Y)
	if _, err := client.RecoverRemotePath(ctx, "", root, key, wrongQ); err == nil || !strings.Contains(err.Error(), "local Q") {
		t.Fatalf("wrong local Q error = %v", err)
	}
}

func TestRecoveryManagerRejectsInsufficientOfferBeforeAggregate(t *testing.T) {
	serverStore, root, key, _, expectedQ := buildRecoveryStore(t, 1)
	client := NewRecoveryManager(NewEthDBStore(rawdb.NewMemoryDatabase()))
	server := NewRecoveryManager(serverStore)
	closeNetwork := connectRecoveryManagers(t, server, client)
	defer closeNetwork()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.RecoverRemotePath(ctx, "", root, key, expectedQ)
	if err == nil || !strings.Contains(err.Error(), "not sufficient") {
		t.Fatalf("insufficient offer error = %v", err)
	}
}

func buildRecoveryStore(t *testing.T, rows int) (*EthDBStore, common.Hash, []byte, [][]byte, bn254.G1Affine) {
	t.Helper()
	const start = uint64(20)
	key := bytes.Repeat([]byte{0x42}, 32)
	oldPath := [][]byte{bytes.Repeat([]byte("old-branch/"), 20), bytes.Repeat([]byte("old-leaf/"), 16)}
	finalPath := [][]byte{
		bytes.Repeat([]byte("final-branch/"), 18),
		bytes.Repeat([]byte("final-extension/"), 14),
		bytes.Repeat([]byte("final-leaf-value/"), 12),
	}
	root0 := rootForProcessorHeight(start)
	root1 := rootForProcessorHeight(start + 1)
	resolver := mapPathResolver{
		pathMapKey(root0, key): oldPath,
		pathMapKey(root1, key): finalPath,
	}
	store := NewEthDBStore(rawdb.NewMemoryDatabase())
	processor, err := NewProcessor(ProcessorConfig{EpochStart: start, Seed: []byte("network-recovery-seed"), MinimumRows: rows}, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := processor.RecordHeight(start, root0, nil); err != nil {
		t.Fatal(err)
	}
	if err := processor.RecordHeight(start+1, root1, []KeyUpdate{{Key: key, PreviousExists: true, NewExists: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := processor.Finalize(start+1, [][]byte{key}, resolver); err != nil {
		t.Fatal(err)
	}
	path, ok, err := store.EncodedPath(root1, key)
	if err != nil || !ok {
		t.Fatalf("load generated path: ok=%v err=%v", ok, err)
	}
	return store, root1, key, finalPath, path.Q
}

func connectRecoveryManagers(t *testing.T, server, client *RecoveryManager) func() {
	t.Helper()
	serverTransport, clientTransport, closeTransport := mptproofmsg.NewMemoryTransportPair(8)
	serverHandler := mptproofmsg.NewHandlerMux(nil, server.NewHandler(nil, mptproofmsg.NewSender(serverTransport)))
	clientHandler := mptproofmsg.NewHandlerMux(nil, client.NewHandler(nil, mptproofmsg.NewSender(clientTransport)))
	ctx, cancel := context.WithCancel(context.Background())
	pump := func(transport mptproofmsg.Transport, handler mptproofmsg.Handler) {
		for {
			packet, err := mptproofmsg.NewReceiver(transport).Receive()
			if err != nil {
				return
			}
			if err := mptproofmsg.DispatchPacket(handler, packet); err != nil {
				t.Errorf("dispatch %s: %v", packet.Name(), err)
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}
	go pump(serverTransport, serverHandler)
	go pump(clientTransport, clientHandler)
	return func() {
		cancel()
		closeTransport()
		serverHandler.CloseMPTProofPeer()
		clientHandler.CloseMPTProofPeer()
	}
}
