package mptproofmsg

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	GetCommitmentMsg = 0x00
	CommitmentMsg    = 0x01
	GetProofMsg      = 0x02
	ProofMsg         = 0x03

	GetFoldedFileIPAProofMsg = 0x04
	FoldedFileIPAProofMsg    = 0x05
)

var ErrInvalidMessageCode = errors.New("invalid mpt proof message code")

// Packet represents a message in the experimental MPT proof message set.
type Packet interface {
	Name() string
	Kind() byte
}

type GetCommitmentPacket struct {
	ID      uint64
	Root    common.Hash
	PathKey []byte
}

type CommitmentPacket struct {
	ID         uint64
	Root       common.Hash
	PathKey    []byte
	Commitment []byte
}

type GetProofPacket struct {
	ID      uint64
	Root    common.Hash
	PathKey []byte
}

type ProofPacket struct {
	ID      uint64
	Root    common.Hash
	PathKey []byte
	Proof   []byte
}

type FileRefWire struct {
	Key  []byte
	Hash common.Hash
	Size uint64
}

type GetFoldedFileIPAProofPacket struct {
	ID        uint64
	Root      common.Hash
	Files     []FileRefWire
	CoeffSets [][]ScalarWire
	Domain    string
}

type FoldedFileIPAProofPacket struct {
	ID uint64

	Root  common.Hash
	Files []FileRefWire

	Rows           []FileIPARowWire
	FoldChallenges [][]ScalarWire
	IPAProof       IPAProofWire

	SharedQ      G1Wire
	VectorLength uint64
	ParamsID     string
	Domain       string
}

func (*GetCommitmentPacket) Name() string { return "GetCommitment" }
func (*GetCommitmentPacket) Kind() byte   { return GetCommitmentMsg }

func (*CommitmentPacket) Name() string { return "Commitment" }
func (*CommitmentPacket) Kind() byte   { return CommitmentMsg }

func (*GetProofPacket) Name() string { return "GetProof" }
func (*GetProofPacket) Kind() byte   { return GetProofMsg }

func (*ProofPacket) Name() string { return "Proof" }
func (*ProofPacket) Kind() byte   { return ProofMsg }

func (*GetFoldedFileIPAProofPacket) Name() string { return "GetFoldedFileIPAProof" }
func (*GetFoldedFileIPAProofPacket) Kind() byte   { return GetFoldedFileIPAProofMsg }

func (*FoldedFileIPAProofPacket) Name() string { return "FoldedFileIPAProof" }
func (*FoldedFileIPAProofPacket) Kind() byte   { return FoldedFileIPAProofMsg }

func EncodePacket(packet Packet) (uint64, []byte, error) {
	if packet == nil {
		return 0, nil, errors.New("nil packet")
	}
	payload, err := rlp.EncodeToBytes(packet)
	if err != nil {
		return 0, nil, err
	}
	return uint64(packet.Kind()), payload, nil
}

func DecodePacket(code uint64, payload []byte) (Packet, error) {
	switch code {
	case GetCommitmentMsg:
		var packet GetCommitmentPacket
		if err := rlp.DecodeBytes(payload, &packet); err != nil {
			return nil, err
		}
		return &packet, nil
	case CommitmentMsg:
		var packet CommitmentPacket
		if err := rlp.DecodeBytes(payload, &packet); err != nil {
			return nil, err
		}
		return &packet, nil
	case GetProofMsg:
		var packet GetProofPacket
		if err := rlp.DecodeBytes(payload, &packet); err != nil {
			return nil, err
		}
		return &packet, nil
	case ProofMsg:
		var packet ProofPacket
		if err := rlp.DecodeBytes(payload, &packet); err != nil {
			return nil, err
		}
		return &packet, nil
	case GetFoldedFileIPAProofMsg:
		var packet GetFoldedFileIPAProofPacket
		if err := rlp.DecodeBytes(payload, &packet); err != nil {
			return nil, err
		}
		return &packet, nil
	case FoldedFileIPAProofMsg:
		var packet FoldedFileIPAProofPacket
		if err := rlp.DecodeBytes(payload, &packet); err != nil {
			return nil, err
		}
		return &packet, nil
	default:
		return nil, fmt.Errorf("%w: %d", ErrInvalidMessageCode, code)
	}
}

func RequestID(packet Packet) (uint64, bool) {
	switch packet := packet.(type) {
	case *GetCommitmentPacket:
		return packet.ID, true
	case *CommitmentPacket:
		return packet.ID, true
	case *GetProofPacket:
		return packet.ID, true
	case *ProofPacket:
		return packet.ID, true
	case *GetFoldedFileIPAProofPacket:
		return packet.ID, true
	case *FoldedFileIPAProofPacket:
		return packet.ID, true
	default:
		return 0, false
	}
}
