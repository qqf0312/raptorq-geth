package mptproofp2p

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/internal/mptproofmsg"
	"github.com/ethereum/go-ethereum/p2p"
)

func TestMakeProtocolsRegistersMPTProofCapability(t *testing.T) {
	protocols := MakeProtocols(nil)
	if len(protocols) != 1 {
		t.Fatalf("protocol count = %d, want 1", len(protocols))
	}
	protocol := protocols[0]
	if protocol.Name != ProtocolName {
		t.Fatalf("protocol name = %q, want %q", protocol.Name, ProtocolName)
	}
	if protocol.Version != ProtocolVersion {
		t.Fatalf("protocol version = %d, want %d", protocol.Version, ProtocolVersion)
	}
	if protocol.Length != ProtocolLength {
		t.Fatalf("protocol length = %d, want %d", protocol.Length, ProtocolLength)
	}
	if protocol.Run == nil {
		t.Fatal("protocol Run is nil")
	}
}

func TestTransportSendsAndReceivesProofMessage(t *testing.T) {
	leftRW, rightRW := p2p.MsgPipe()
	defer leftRW.Close()
	defer rightRW.Close()

	left := NewTransport(leftRW)
	right := mptproofmsg.NewReceiver(NewTransport(rightRW))

	want := mptproofmsg.GetFoldedFileIPAProofPacket{
		ID:   7,
		Root: common.HexToHash("0x1234"),
		Files: []mptproofmsg.FileRefWire{{
			Key:  []byte("path/0"),
			Hash: common.HexToHash("0xabcd"),
			Size: 32,
		}},
		Domain: "mptproofp2p-test",
	}
	go func() {
		if err := left.Send(uint64(want.Kind()), &want); err != nil {
			t.Errorf("Send: %v", err)
		}
	}()

	gotPacket, err := right.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	got, ok := gotPacket.(*mptproofmsg.GetFoldedFileIPAProofPacket)
	if !ok {
		t.Fatalf("packet type %T, want *GetFoldedFileIPAProofPacket", gotPacket)
	}
	if got.ID != want.ID || got.Root != want.Root || got.Domain != want.Domain {
		t.Fatalf("packet metadata mismatch: got id=%d root=%s domain=%q", got.ID, got.Root, got.Domain)
	}
	if len(got.Files) != 1 || string(got.Files[0].Key) != "path/0" || got.Files[0].Hash != common.HexToHash("0xabcd") {
		t.Fatalf("packet files mismatch: %+v", got.Files)
	}
}
