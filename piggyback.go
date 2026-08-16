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

	"github.com/pion/stun/v3"
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
	PiggybackingStateTentative piggybackingState = iota
	PiggybackingStateConfirmed
	PiggybackingStatePending
	PiggybackingStateComplete
	PiggybackingStateOff
)

// DTLS-in-STUN controller.
type piggybackingController struct {
	mu           sync.Mutex
	state        piggybackingState
	packets      []packetWithCrc
	packetsIndex int
	acks         []uint32
	dtlsCallback func(packet []byte, rAddr net.Addr)
	newFlight    bool
	connected    bool
}

// init sets the controller to its initial off state. SetDtlsCallback flips it
// to tentative when piggybacking is enabled.
func (p *piggybackingController) init() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.acks = []uint32{}
	p.state = PiggybackingStateOff
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
func (a *Agent) SetDtlsHandshakeComplete(isClient, isDtls13 bool) {
	a.piggyback.mu.Lock()
	defer a.piggyback.mu.Unlock()
	if a.piggyback.state == PiggybackingStateOff || a.piggyback.state == PiggybackingStateComplete {
		return
	}
	if isClient != isDtls13 {
		a.piggyback.packets = []packetWithCrc{}
		a.piggyback.packetsIndex = 0
	}
	a.piggyback.state = PiggybackingStatePending
}

// Piggyback stores a packet to be picked in a round-robin fashion.
// Returns `true` if packet is to be consumed.
func (a *Agent) Piggyback(packet []byte, end bool) bool {
	a.piggyback.mu.Lock()
	defer a.piggyback.mu.Unlock()
	if a.piggyback.state == PiggybackingStateOff && a.piggyback.connected {
		return false
	}

	if packet != nil {
		if !isDtlsPacket(packet) {
			return false
		}
		// If we receive a packet after the end of a flight we need
		// to clear the outgoing list.
		if a.piggyback.newFlight {
			a.piggyback.packets = []packetWithCrc{}
			a.piggyback.packetsIndex = 0
		}
		a.piggyback.newFlight = end
		crc := crc32.ChecksumIEEE(packet)
		// Copy the packet as the caller may reuse the underlying buffer.
		data := bytes.Clone(packet)
		a.piggyback.packets = append(a.piggyback.packets, packetWithCrc{data, crc})
	}
	// If we are connected we could send DTLS plain.
	return true
}

// GetPiggybackDataAndAcks returns a packet from the stored list in a round-robin fashion and a list of acks.
func (a *Agent) GetPiggybackDataAndAcks() ([]byte, []uint32) {
	a.piggyback.mu.Lock()
	defer a.piggyback.mu.Unlock()

	if a.piggyback.state == PiggybackingStateOff || a.piggyback.state == PiggybackingStateComplete {
		return nil, nil
	}
	if len(a.piggyback.packets) == 0 {
		return nil, slices.Clone(a.piggyback.acks)
	}

	packet := a.piggyback.packets[a.piggyback.packetsIndex]
	a.piggyback.packetsIndex = (a.piggyback.packetsIndex + 1) % len(a.piggyback.packets)

	// Return copies to prevent external modification of the internal buffers.
	result := make([]byte, len(packet.data))
	copy(result, packet.data)

	return result, slices.Clone(a.piggyback.acks)
}

func (a *Agent) ReportPiggybacking(packet []byte, acks []uint32, rAddr net.Addr) { //nolint:cyclop
	a.piggyback.mu.Lock()

	if a.piggyback.state == PiggybackingStateComplete || a.piggyback.state == PiggybackingStateOff {
		a.piggyback.mu.Unlock()

		return
	}
	if packet == nil && acks == nil && a.piggyback.state == PiggybackingStateTentative {
		// Any pending packets will be flushed later when the ICE connection gets established.
		a.log.Infof("Piggybacking discovered as not supported, falling back to normal state")
		a.piggyback.dtlsCallback = nil
		a.piggyback.state = PiggybackingStateOff
		a.piggyback.mu.Unlock()

		return
	}
	// The peer may have stopped sending acks when it moved to the complete
	// state. Move to the same state.
	if packet == nil && acks == nil && a.piggyback.state == PiggybackingStatePending {
		a.log.Info("Done with the SPED handshake")
		a.piggyback.acks = nil
		a.piggyback.state = PiggybackingStateComplete
		a.piggyback.mu.Unlock()

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
		if a.piggyback.packetsIndex >= removed {
			a.piggyback.packetsIndex -= removed
		} else {
			a.piggyback.packetsIndex = 0
		}
	}
	// The response to the final flight will not contain DTLS data but an ack.
	if packet == nil && acks != nil && a.piggyback.state == PiggybackingStatePending {
		a.log.Info("Done with the SPED handshake")
		a.piggyback.acks = nil
		a.piggyback.state = PiggybackingStateComplete
		a.piggyback.mu.Unlock()

		return
	}
	if len(packet) > 0 && !isDtlsPacket(packet) {
		a.log.Warn("Dropping non-DTLS data")
		a.piggyback.mu.Unlock()

		return
	}

	var dtlsCallback func(packet []byte, rAddr net.Addr)
	// Handle the incoming packet. Calculate and store the crc32 of the packet
	// for acks, then notify the DTLS packet.
	if a.piggyback.dtlsCallback != nil && len(packet) > 0 {
		crc := crc32.ChecksumIEEE(packet)
		if !slices.Contains(a.piggyback.acks, crc) {
			a.piggyback.acks = append(a.piggyback.acks, crc)
			if len(a.piggyback.acks) > 4 {
				a.piggyback.acks = a.piggyback.acks[1:]
			}
		}
		dtlsCallback = a.piggyback.dtlsCallback
	}

	a.piggyback.mu.Unlock()

	if dtlsCallback != nil {
		dtlsCallback(packet, rAddr)
	}
}

// appendPiggybackAttributes appends DTLS-in-STUN and ACK attributes (when
// available) to the given setter slice. It is the single place that knows
// the wire-order of those attributes in outgoing STUN messages.
func (a *Agent) appendPiggybackAttributes(attrs []stun.Setter) []stun.Setter {
	packet, acks := a.GetPiggybackDataAndAcks()
	if acks == nil {
		return attrs
	}
	attrs = append(attrs, DtlsInStunAckAttribute(acks))
	if packet != nil {
		attrs = append(attrs, DtlsInStunAttribute(packet))
	}

	return attrs
}

// reportPiggybackingFromMessage extracts the DTLS-in-STUN payload and ACK list
// from a STUN message and forwards them to the controller.
func (a *Agent) reportPiggybackingFromMessage(message *stun.Message, remote Candidate) {
	var dtls DtlsInStunAttribute
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

	if a.piggyback.state == PiggybackingStateComplete || a.piggyback.state == PiggybackingStateOff {
		a.piggyback.mu.Unlock()

		return
	}
	crc := crc32.ChecksumIEEE(packet)
	if !slices.Contains(a.piggyback.acks, crc) {
		a.piggyback.acks = append(a.piggyback.acks, crc)
		if len(a.piggyback.acks) > 4 {
			a.piggyback.acks = a.piggyback.acks[1:]
		}
	}
	a.piggyback.mu.Unlock()
}
