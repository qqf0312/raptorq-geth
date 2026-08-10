package mptproofmsg

import "errors"

var ErrTransportClosed = errors.New("mpt proof transport closed")

type WireMessage struct {
	Code    uint64
	Payload []byte
}

type Transport interface {
	Send(code uint64, packet any) error
	Receive() (code uint64, payload []byte, err error)
}

type Sender interface {
	SendGetCommitment(req GetCommitmentPacket) error
	SendCommitment(res CommitmentPacket) error
	SendGetProof(req GetProofPacket) error
	SendProof(res ProofPacket) error
	SendGetFoldedFileIPAProof(req GetFoldedFileIPAProofPacket) error
	SendFoldedFileIPAProof(res FoldedFileIPAProofPacket) error
}

// FountainSender is optional and keeps the original Sender interface stable
// for existing proof transports and test doubles.
type FountainSender interface {
	SendGetFountainOffer(req GetFountainOfferPacket) error
	SendFountainOffer(res FountainOfferPacket) error
	SendGetFountainAggregate(req GetFountainAggregatePacket) error
	SendFountainAggregate(res FountainAggregatePacket) error
}

type Receiver interface {
	Receive() (Packet, error)
}

type MessageSender struct {
	transport Transport
}

func NewSender(transport Transport) *MessageSender {
	return &MessageSender{transport: transport}
}

func (s *MessageSender) SendGetCommitment(req GetCommitmentPacket) error {
	return s.transport.Send(GetCommitmentMsg, &req)
}

func (s *MessageSender) SendCommitment(res CommitmentPacket) error {
	return s.transport.Send(CommitmentMsg, &res)
}

func (s *MessageSender) SendGetProof(req GetProofPacket) error {
	return s.transport.Send(GetProofMsg, &req)
}

func (s *MessageSender) SendProof(res ProofPacket) error {
	return s.transport.Send(ProofMsg, &res)
}

func (s *MessageSender) SendGetFoldedFileIPAProof(req GetFoldedFileIPAProofPacket) error {
	return s.transport.Send(GetFoldedFileIPAProofMsg, &req)
}

func (s *MessageSender) SendFoldedFileIPAProof(res FoldedFileIPAProofPacket) error {
	return s.transport.Send(FoldedFileIPAProofMsg, &res)
}

func (s *MessageSender) SendGetFountainOffer(req GetFountainOfferPacket) error {
	return s.transport.Send(GetFountainOfferMsg, &req)
}

func (s *MessageSender) SendFountainOffer(res FountainOfferPacket) error {
	return s.transport.Send(FountainOfferMsg, &res)
}

func (s *MessageSender) SendGetFountainAggregate(req GetFountainAggregatePacket) error {
	return s.transport.Send(GetFountainAggregateMsg, &req)
}

func (s *MessageSender) SendFountainAggregate(res FountainAggregatePacket) error {
	return s.transport.Send(FountainAggregateMsg, &res)
}

type MessageReceiver struct {
	transport Transport
}

func NewReceiver(transport Transport) *MessageReceiver {
	return &MessageReceiver{transport: transport}
}

func (r *MessageReceiver) Receive() (Packet, error) {
	code, payload, err := r.transport.Receive()
	if err != nil {
		return nil, err
	}
	return DecodePacket(code, payload)
}

type MemoryTransport struct {
	in     <-chan WireMessage
	out    chan<- WireMessage
	closed <-chan struct{}
}

func NewMemoryTransport(in <-chan WireMessage, out chan<- WireMessage, closed <-chan struct{}) *MemoryTransport {
	return &MemoryTransport{in: in, out: out, closed: closed}
}

func NewMemoryTransportPair(buffer int) (*MemoryTransport, *MemoryTransport, func()) {
	aToB := make(chan WireMessage, buffer)
	bToA := make(chan WireMessage, buffer)
	closed := make(chan struct{})
	closeFn := func() {
		select {
		case <-closed:
		default:
			close(closed)
		}
	}
	return NewMemoryTransport(bToA, aToB, closed), NewMemoryTransport(aToB, bToA, closed), closeFn
}

func (t *MemoryTransport) Send(code uint64, packet any) error {
	payload, err := encodeAny(packet)
	if err != nil {
		return err
	}
	msg := WireMessage{Code: code, Payload: payload}
	if t.closed == nil {
		t.out <- msg
		return nil
	}
	select {
	case <-t.closed:
		return ErrTransportClosed
	default:
	}
	select {
	case t.out <- msg:
		return nil
	case <-t.closed:
		return ErrTransportClosed
	}
}

func (t *MemoryTransport) Receive() (uint64, []byte, error) {
	if t.closed == nil {
		msg, ok := <-t.in
		if !ok {
			return 0, nil, ErrTransportClosed
		}
		return msg.Code, append([]byte(nil), msg.Payload...), nil
	}
	select {
	case msg, ok := <-t.in:
		if !ok {
			return 0, nil, ErrTransportClosed
		}
		return msg.Code, append([]byte(nil), msg.Payload...), nil
	case <-t.closed:
		return 0, nil, ErrTransportClosed
	}
}
