package mptproofmsg

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/rlp"
)

type Handler interface {
	HandleGetCommitment(*GetCommitmentPacket) error
	HandleCommitment(*CommitmentPacket) error
	HandleGetProof(*GetProofPacket) error
	HandleProof(*ProofPacket) error
	HandleGetFoldedFileIPAProof(*GetFoldedFileIPAProofPacket) error
	HandleFoldedFileIPAProof(*FoldedFileIPAProofPacket) error
}

func HandlePacket(handler Handler, code uint64, payload []byte) error {
	if handler == nil {
		return errors.New("nil handler")
	}
	packet, err := DecodePacket(code, payload)
	if err != nil {
		return err
	}
	return DispatchPacket(handler, packet)
}

func DispatchPacket(handler Handler, packet Packet) error {
	if handler == nil {
		return errors.New("nil handler")
	}
	switch packet := packet.(type) {
	case *GetCommitmentPacket:
		return handler.HandleGetCommitment(packet)
	case *CommitmentPacket:
		return handler.HandleCommitment(packet)
	case *GetProofPacket:
		return handler.HandleGetProof(packet)
	case *ProofPacket:
		return handler.HandleProof(packet)
	case *GetFoldedFileIPAProofPacket:
		return handler.HandleGetFoldedFileIPAProof(packet)
	case *FoldedFileIPAProofPacket:
		return handler.HandleFoldedFileIPAProof(packet)
	default:
		return fmt.Errorf("unknown mpt proof packet type %T", packet)
	}
}

func encodeAny(packet any) ([]byte, error) {
	if packet == nil {
		return nil, errors.New("nil packet")
	}
	return rlp.EncodeToBytes(packet)
}
