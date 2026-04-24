// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package ice implements the Interactive Connectivity Establishment (ICE)
// protocol defined in rfc5245.
package ice

import (
	"bytes"
	"errors"
	"hash/crc32"
	"net"
	"slices"
	"sync"

	"github.com/pion/dtls/v3/pkg/protocol"
	"github.com/pion/stun/v4"
)

type packetWithCrc struct {
	data []byte
	crc  uint32
}

const dtlsRecordHeaderLen = 13

// isDtlsPacket determines whether the payload is a DTLS record.
func isDtlsPacket(payload []byte) bool {
	return len(payload) >= dtlsRecordHeaderLen && payload[0] > 19 && payload[0] < 64
}

type piggybackingState int

const (
	PiggybackingStateOff piggybackingState = iota
	PiggybackingStateTentative
	PiggybackingStateConfirmed
	PiggybackingStatePending
	PiggybackingStateComplete
)

// DTLS-in-STUN controller.
type piggybackingController struct {
	mu           sync.Mutex
	state        piggybackingState
	packets      []packetWithCrc
	packetsIndex int
	acks         []uint32
	dtlsCallback func(packet []byte, rAddr net.Addr)
	connected    bool
}

// flushOnConnected returns any pending packets that need to be sent as plain
// DTLS once the ICE connection is established with piggybacking disabled.
func (p *piggybackingController) flushOnConnected() []packetWithCrc {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.connected = true
	if p.state != PiggybackingStateOff {
		return nil
	}
	packets := p.packets
	p.packets = []packetWithCrc{}

	return packets
}

// SetDtlsCallback sets the callback for DTLS packets. Setting this callback
// initializes state of the piggybacking state machine to "tentative", i.e.
// expecting embedded packets.
func (a *Agent) SetDtlsCallback(cb func(packet []byte, rAddr net.Addr)) {
	a.piggyback.mu.Lock()
	defer a.piggyback.mu.Unlock()

	a.piggyback.dtlsCallback = cb
	if cb != nil {
		if a.piggyback.acks == nil {
			a.piggyback.acks = []uint32{}
		}
		a.piggyback.state = PiggybackingStateTentative
	}
}

// SetDtlsFailed disables piggybacking after the DTLS handshake failed.
func (a *Agent) SetDtlsFailed() {
	a.piggyback.mu.Lock()
	defer a.piggyback.mu.Unlock()

	if a.piggyback.state != PiggybackingStateComplete && a.piggyback.state != PiggybackingStateOff {
		a.log.Info("DTLS failed during negotiation, disabling piggybacking")
	}
	a.piggyback.state = PiggybackingStateOff
}

// SetDtlsHandshakeComplete signals that the local DTLS handshake completed and
// carries the negotiated DTLS role and version. The party that sends the last
// flight has to keep it around until it gets acknowledged; that is the server
// in DTLS 1.2 and the client in DTLS 1.3. The other party has nothing more to
// send and drops its outgoing packets.
func (a *Agent) SetDtlsHandshakeComplete(isClient bool, version protocol.Version) {
	a.piggyback.mu.Lock()
	defer a.piggyback.mu.Unlock()

	if a.piggyback.state == PiggybackingStateOff || a.piggyback.state == PiggybackingStateComplete {
		return
	}
	if isClient != (version == protocol.Version1_3) {
		a.piggyback.packets = []packetWithCrc{}
		a.piggyback.packetsIndex = 0
	}
	a.piggyback.state = PiggybackingStatePending
}

// Piggyback stores the datagrams of one DTLS flight, to be picked in a
// round-robin fashion. Returns `true` if the flight is to be consumed.
func (a *Agent) Piggyback(datagrams [][]byte, _ net.Addr) bool {
	a.piggyback.mu.Lock()
	defer a.piggyback.mu.Unlock()

	if a.piggyback.state == PiggybackingStateOff && a.piggyback.connected {
		return false
	}

	if len(datagrams) > 0 {
		// Refuse the whole flight rather than embed it in part.
		for _, datagram := range datagrams {
			if !isDtlsPacket(datagram) {
				return false
			}
		}
		// A new flight replaces the outgoing list.
		a.piggyback.packets = a.piggyback.packets[:0]
		a.piggyback.packetsIndex = 0
		for _, datagram := range datagrams {
			// Copy the datagram as the caller may reuse the underlying buffer.
			a.piggyback.packets = append(a.piggyback.packets,
				packetWithCrc{bytes.Clone(datagram), crc32.ChecksumIEEE(datagram)})
		}
	}
	// If we are connected also send DTLS plain.
	return !a.piggyback.connected
}

func (a *Agent) ReportPiggybacking(packet []byte, acks []uint32, rAddr net.Addr) { //nolint:cyclop
	var dtlsCallback func(packet []byte, rAddr net.Addr)
	a.piggyback.mu.Lock()
	defer func() {
		a.piggyback.mu.Unlock()
		if dtlsCallback != nil {
			dtlsCallback(packet, rAddr)
		}
	}()

	if a.piggyback.state == PiggybackingStateComplete || a.piggyback.state == PiggybackingStateOff {
		return
	}
	if packet == nil && acks == nil && a.piggyback.state == PiggybackingStateTentative {
		// Any pending packets will be flushed later when the ICE connection gets established.
		a.log.Infof("Piggybacking discovered as not supported, falling back to normal state")
		a.piggyback.dtlsCallback = nil
		a.piggyback.state = PiggybackingStateOff

		return
	}
	if a.piggyback.state == PiggybackingStateTentative {
		a.piggyback.state = PiggybackingStateConfirmed
	}
	// Handle incoming acks.
	if size := len(acks); size > 0 {
		beforeLen := len(a.piggyback.packets)
		a.piggyback.packets = slices.DeleteFunc(a.piggyback.packets, func(p packetWithCrc) bool {
			// Remove packets that were acknowledged.
			return slices.Contains(acks, p.crc)
		})
		removed := beforeLen - len(a.piggyback.packets)

		// Adjust the index if it's out of bounds after deletion
		a.piggyback.packetsIndex = max(0, a.piggyback.packetsIndex-removed)
	}
	// Complete when the peer acknowledges the final flight or stops sending acks.
	if packet == nil && a.piggyback.state == PiggybackingStatePending {
		a.log.Info("Done with the SPED handshake")
		a.piggyback.acks = nil
		a.piggyback.state = PiggybackingStateComplete

		return
	}
	if len(packet) > 0 && !isDtlsPacket(packet) {
		a.log.Warn("Dropping non-DTLS data")

		return
	}

	// Handle the incoming packet. Calculate and store the crc32 of the packet
	// for acks, then notify the DTLS packet.
	if a.piggyback.dtlsCallback != nil && len(packet) > 0 {
		a.piggyback.acknowledge(packet)
		dtlsCallback = a.piggyback.dtlsCallback
	}
}

// appendPiggybackAttributes appends DTLS-in-STUN and ACK attributes (when
// available) to the given setter slice. It is the single place that knows
// the wire-order of those attributes in outgoing STUN messages.
func (a *Agent) appendPiggybackAttributes(attrs []stun.Setter) []stun.Setter {
	p := &a.piggyback
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.state == PiggybackingStateOff || p.state == PiggybackingStateComplete {
		return attrs
	}
	// Copy buffers because the attributes are encoded after releasing the lock.
	attrs = append(attrs, DtlsInStunAckAttribute(slices.Clone(p.acks)))
	if len(p.packets) > 0 {
		attrs = append(attrs, DTLSInSTUNAttribute(bytes.Clone(p.packets[p.packetsIndex].data)))
		p.packetsIndex = (p.packetsIndex + 1) % len(p.packets)
	} else if p.state == PiggybackingStateConfirmed {
		// Empty data signals support without signaling handshake completion.
		attrs = append(attrs, DTLSInSTUNAttribute{})
	}

	return attrs
}

// reportPiggybackingFromMessage extracts the DTLS-in-STUN payload and ACK list
// from a STUN message and forwards them to the controller.
func (a *Agent) reportPiggybackingFromMessage(message *stun.Message, remote Candidate) {
	var dtls DTLSInSTUNAttribute
	_ = dtls.GetFrom(message)
	var ack DtlsInStunAckAttribute
	// A malformed attribute must not be treated like an absent one which signals
	// a peer without piggybacking support, drop the message instead.
	if err := ack.GetFrom(message); err != nil && !errors.Is(err, stun.ErrAttributeNotFound) {
		a.log.Warnf("Discarding malformed DTLS-in-STUN ack attribute: %v", err)

		return
	}
	a.ReportPiggybacking(dtls, ack, remote.addr())
}

func (a *Agent) ReportDtlsPacket(packet []byte) {
	a.piggyback.mu.Lock()
	defer a.piggyback.mu.Unlock()

	if a.piggyback.state == PiggybackingStateComplete || a.piggyback.state == PiggybackingStateOff {
		return
	}
	a.piggyback.acknowledge(packet)
}

// acknowledge records a packet while the controller mutex is held.
func (p *piggybackingController) acknowledge(packet []byte) {
	crc := crc32.ChecksumIEEE(packet)
	if !slices.Contains(p.acks, crc) {
		p.acks = append(p.acks, crc)
		if len(p.acks) > ackSizeValues {
			p.acks = p.acks[1:]
		}
	}
}
