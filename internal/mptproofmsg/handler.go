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

// FountainHandler is optional so existing MPT proof handlers keep their
// public interface and behavior unchanged.
type FountainHandler interface {
	HandleGetFountainOffer(*GetFountainOfferPacket) error
	HandleFountainOffer(*FountainOfferPacket) error
	HandleGetFountainAggregate(*GetFountainAggregatePacket) error
	HandleFountainAggregate(*FountainAggregatePacket) error
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
	case *GetFountainOfferPacket:
		return dispatchFountain(handler, func(h FountainHandler) error { return h.HandleGetFountainOffer(packet) })
	case *FountainOfferPacket:
		return dispatchFountain(handler, func(h FountainHandler) error { return h.HandleFountainOffer(packet) })
	case *GetFountainAggregatePacket:
		return dispatchFountain(handler, func(h FountainHandler) error { return h.HandleGetFountainAggregate(packet) })
	case *FountainAggregatePacket:
		return dispatchFountain(handler, func(h FountainHandler) error { return h.HandleFountainAggregate(packet) })
	default:
		return fmt.Errorf("unknown mpt proof packet type %T", packet)
	}
}

func dispatchFountain(handler Handler, call func(FountainHandler) error) error {
	fountain, ok := handler.(FountainHandler)
	if !ok {
		return fmt.Errorf("mpt proof handler does not support fountain packets")
	}
	return call(fountain)
}

func encodeAny(packet any) ([]byte, error) {
	if packet == nil {
		return nil, errors.New("nil packet")
	}
	return rlp.EncodeToBytes(packet)
}
