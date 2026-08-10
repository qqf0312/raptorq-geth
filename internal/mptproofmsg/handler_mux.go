package mptproofmsg

import "fmt"

// HandlerMux keeps the original proof handler API intact while optionally
// attaching the fountain recovery messages to the same devp2p capability.
type HandlerMux struct {
	Legacy   Handler
	Fountain FountainHandler
}

func NewHandlerMux(legacy Handler, fountain FountainHandler) *HandlerMux {
	return &HandlerMux{Legacy: legacy, Fountain: fountain}
}

func (m *HandlerMux) HandleGetCommitment(packet *GetCommitmentPacket) error {
	if m.Legacy == nil {
		return nil
	}
	return m.Legacy.HandleGetCommitment(packet)
}

func (m *HandlerMux) HandleCommitment(packet *CommitmentPacket) error {
	if m.Legacy == nil {
		return nil
	}
	return m.Legacy.HandleCommitment(packet)
}

func (m *HandlerMux) HandleGetProof(packet *GetProofPacket) error {
	if m.Legacy == nil {
		return nil
	}
	return m.Legacy.HandleGetProof(packet)
}

func (m *HandlerMux) HandleProof(packet *ProofPacket) error {
	if m.Legacy == nil {
		return nil
	}
	return m.Legacy.HandleProof(packet)
}

func (m *HandlerMux) HandleGetFoldedFileIPAProof(packet *GetFoldedFileIPAProofPacket) error {
	if m.Legacy == nil {
		return nil
	}
	return m.Legacy.HandleGetFoldedFileIPAProof(packet)
}

func (m *HandlerMux) HandleFoldedFileIPAProof(packet *FoldedFileIPAProofPacket) error {
	if m.Legacy == nil {
		return nil
	}
	return m.Legacy.HandleFoldedFileIPAProof(packet)
}

func (m *HandlerMux) HandleGetFountainOffer(packet *GetFountainOfferPacket) error {
	if m.Fountain == nil {
		return fmt.Errorf("fountain recovery handler is not enabled")
	}
	return m.Fountain.HandleGetFountainOffer(packet)
}

func (m *HandlerMux) HandleFountainOffer(packet *FountainOfferPacket) error {
	if m.Fountain == nil {
		return fmt.Errorf("fountain recovery handler is not enabled")
	}
	return m.Fountain.HandleFountainOffer(packet)
}

func (m *HandlerMux) HandleGetFountainAggregate(packet *GetFountainAggregatePacket) error {
	if m.Fountain == nil {
		return fmt.Errorf("fountain recovery handler is not enabled")
	}
	return m.Fountain.HandleGetFountainAggregate(packet)
}

func (m *HandlerMux) HandleFountainAggregate(packet *FountainAggregatePacket) error {
	if m.Fountain == nil {
		return fmt.Errorf("fountain recovery handler is not enabled")
	}
	return m.Fountain.HandleFountainAggregate(packet)
}

func (m *HandlerMux) CloseMPTProofPeer() {
	if closer, ok := m.Legacy.(interface{ CloseMPTProofPeer() }); ok {
		closer.CloseMPTProofPeer()
	}
	if closer, ok := m.Fountain.(interface{ CloseMPTProofPeer() }); ok {
		closer.CloseMPTProofPeer()
	}
}
