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

	GetFountainOfferMsg     = 0x06
	FountainOfferMsg        = 0x07
	GetFountainAggregateMsg = 0x08
	FountainAggregateMsg    = 0x09
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

// GetFountainOfferPacket asks for the small, deterministic description needed
// to decide whether the encoded rows for (Root, PathKey) are recoverable. It
// intentionally does not request encoded C values or an IPA proof.
type GetFountainOfferPacket struct {
	ID      uint64
	Root    common.Hash
	PathKey []byte
}

// FountainOfferPacket describes one persisted fountain path. Matrix entries
// are omitted because the receiver regenerates them from Seed.
type FountainOfferPacket struct {
	ID              uint64
	Found           bool
	Height          uint64
	Seed            []byte
	SourceCount     uint64
	RowCount        uint64
	NodeCount       uint64
	CompactBytes    uint64
	SegmentOffset   uint64
	WitnessLength   uint64
	VectorLength    uint64
	AggregateHeight uint64
	AggregateID     common.Hash
}

// GetFountainAggregatePacket fetches the existing node/epoch aggregate. The
// bundle contains the public statements, segment commitments and the original
// aggregate IPA proof; it does not create a new per-key proof.
type GetFountainAggregatePacket struct {
	ID          uint64
	Height      uint64
	AggregateID common.Hash
}

type FountainAggregatePacket struct {
	ID          uint64
	Found       bool
	Height      uint64
	AggregateID common.Hash
	Bundle      []byte
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

func (*GetFountainOfferPacket) Name() string { return "GetFountainOffer" }
func (*GetFountainOfferPacket) Kind() byte   { return GetFountainOfferMsg }

func (*FountainOfferPacket) Name() string { return "FountainOffer" }
func (*FountainOfferPacket) Kind() byte   { return FountainOfferMsg }

func (*GetFountainAggregatePacket) Name() string { return "GetFountainAggregate" }
func (*GetFountainAggregatePacket) Kind() byte   { return GetFountainAggregateMsg }

func (*FountainAggregatePacket) Name() string { return "FountainAggregate" }
func (*FountainAggregatePacket) Kind() byte   { return FountainAggregateMsg }

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
	case GetFountainOfferMsg:
		var packet GetFountainOfferPacket
		if err := rlp.DecodeBytes(payload, &packet); err != nil {
			return nil, err
		}
		return &packet, nil
	case FountainOfferMsg:
		var packet FountainOfferPacket
		if err := rlp.DecodeBytes(payload, &packet); err != nil {
			return nil, err
		}
		return &packet, nil
	case GetFountainAggregateMsg:
		var packet GetFountainAggregatePacket
		if err := rlp.DecodeBytes(payload, &packet); err != nil {
			return nil, err
		}
		return &packet, nil
	case FountainAggregateMsg:
		var packet FountainAggregatePacket
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
	case *GetFountainOfferPacket:
		return packet.ID, true
	case *FountainOfferPacket:
		return packet.ID, true
	case *GetFountainAggregatePacket:
		return packet.ID, true
	case *FountainAggregatePacket:
		return packet.ID, true
	default:
		return 0, false
	}
}
