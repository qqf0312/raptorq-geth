package mptproofp2p

import (
	"fmt"
	"io"

	"github.com/ethereum/go-ethereum/internal/mptproofmsg"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

const (
	ProtocolName    = "mptproof"
	ProtocolVersion = 1
	ProtocolLength  = mptproofmsg.FountainAggregateMsg + 1
)

type NodeInfo struct {
	Protocol string `json:"protocol"`
	Version  uint   `json:"version"`
}

type PeerInfo struct {
	ID string `json:"id"`
}

type HandlerFactory func(peer *p2p.Peer, sender mptproofmsg.Sender) mptproofmsg.Handler

type peerLifecycleHandler interface {
	CloseMPTProofPeer()
}

// MakeProtocols registers the experimental MPT proof protocol as an independent
// devp2p capability. The handler factory may be nil, in which case packets are
// decoded and logged but no request/response action is taken.
func MakeProtocols(handlerFactory HandlerFactory) []p2p.Protocol {
	return []p2p.Protocol{{
		Name:    ProtocolName,
		Version: ProtocolVersion,
		Length:  ProtocolLength,
		Run: func(peer *p2p.Peer, rw p2p.MsgReadWriter) error {
			return RunPeer(peer, rw, handlerFactory)
		},
		NodeInfo: func() interface{} {
			return NodeInfo{Protocol: ProtocolName, Version: ProtocolVersion}
		},
		PeerInfo: func(id enode.ID) interface{} {
			return PeerInfo{ID: id.String()}
		},
	}}
}

func RunPeer(peer *p2p.Peer, rw p2p.MsgReadWriter, handlerFactory HandlerFactory) error {
	transport := NewTransport(rw)
	var handler mptproofmsg.Handler
	if handlerFactory != nil {
		handler = handlerFactory(peer, mptproofmsg.NewSender(transport))
	}
	if closer, ok := handler.(peerLifecycleHandler); ok {
		defer closer.CloseMPTProofPeer()
	}
	receiver := mptproofmsg.NewReceiver(transport)
	for {
		packet, err := receiver.Receive()
		if err != nil {
			return err
		}
		requestID, _ := mptproofmsg.RequestID(packet)
		log.Debug("Received MPT proof packet", "peer", peerLogID(peer), "packet", packet.Name(), "id", requestID)
		if handler == nil {
			continue
		}
		if err := mptproofmsg.DispatchPacket(handler, packet); err != nil {
			return err
		}
	}
}

func peerLogID(peer *p2p.Peer) string {
	if peer == nil {
		return "unknown"
	}
	return peer.ID().String()
}

type Transport struct {
	rw p2p.MsgReadWriter
}

func NewTransport(rw p2p.MsgReadWriter) *Transport {
	return &Transport{rw: rw}
}

func (t *Transport) Send(code uint64, packet any) error {
	if t == nil || t.rw == nil {
		return fmt.Errorf("nil mptproof p2p transport")
	}
	if code >= ProtocolLength {
		return fmt.Errorf("mptproof message code %d exceeds protocol length %d", code, ProtocolLength)
	}
	return p2p.Send(t.rw, code, packet)
}

func (t *Transport) Receive() (uint64, []byte, error) {
	if t == nil || t.rw == nil {
		return 0, nil, fmt.Errorf("nil mptproof p2p transport")
	}
	msg, err := t.rw.ReadMsg()
	if err != nil {
		return 0, nil, err
	}
	if msg.Code >= ProtocolLength {
		_ = msg.Discard()
		return 0, nil, fmt.Errorf("mptproof message code %d exceeds protocol length %d", msg.Code, ProtocolLength)
	}
	payload, err := io.ReadAll(msg.Payload)
	if err != nil {
		return 0, nil, err
	}
	return msg.Code, payload, nil
}
