// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package ice implements the Interactive Connectivity Establishment (ICE)
// protocol defined in rfc5245.
package ice

import (
	"hash/crc32"
	"net"
	"slices"
	"sync"
)

type packetWithCrc struct {
	data []byte
	crc  uint32
}

type piggybackingState int

const (
	PiggybackingStateTentative = iota
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
}

func clonePiggybackPacket(packet []byte) []byte {
	if packet == nil {
		return nil
	}

	result := make([]byte, len(packet))
	copy(result, packet)

	return result
}

func clonePiggybackAcks(acks []uint32) []uint32 {
	if acks == nil {
		return nil
	}

	result := make([]uint32, len(acks))
	copy(result, acks)

	return result
}

func (p *piggybackingController) resetLocked(state piggybackingState, cb func(packet []byte, rAddr net.Addr)) {
	p.state = state
	p.packets = []packetWithCrc{}
	p.packetsIndex = 0
	p.acks = []uint32{}
	p.dtlsCallback = cb
	p.newFlight = false
}

func (p *piggybackingController) finishLocked() {
	p.state = PiggybackingStatePending
	p.packets = []packetWithCrc{}
	p.packetsIndex = 0
	if p.acks == nil {
		p.acks = []uint32{}
	}
	p.dtlsCallback = nil
	p.newFlight = false
}

func (p *piggybackingController) queuePacketLocked(packet []byte) {
	crc := crc32.ChecksumIEEE(packet)
	p.packets = append(p.packets, packetWithCrc{clonePiggybackPacket(packet), crc})
}

// SetDtlsCallback sets the callback for DTLS packets. Setting this callback
// initializes state of the piggybacking state machine to "tentative", i.e.
// expecting embedded packets.
func (a *Agent) SetDtlsCallback(cb func(packet []byte, rAddr net.Addr)) {
	a.piggyback.mu.Lock()
	defer a.piggyback.mu.Unlock()
	if cb != nil {
		a.piggyback.resetLocked(PiggybackingStateTentative, cb)

		return
	}

	if a.piggyback.state == PiggybackingStatePending {
		a.piggyback.finishLocked()

		return
	}

	a.piggyback.resetLocked(PiggybackingStateOff, nil)
}

// Piggyback stores a packet to be picked in a round-robin fashion.
// Returns `true` if packet is to be consumed.
func (a *Agent) Piggyback(packet []byte, end bool) bool {
	a.piggyback.mu.Lock()
	defer a.piggyback.mu.Unlock()
	if a.piggyback.state == PiggybackingStateOff || a.piggyback.state == PiggybackingStateComplete {
		if a.connectionState == ConnectionStateConnected {
			return false
		}
		if packet != nil {
			if a.piggyback.newFlight {
				a.piggyback.packets = []packetWithCrc{}
				a.piggyback.packetsIndex = 0
			}
			a.piggyback.newFlight = end
			a.piggyback.queuePacketLocked(packet)
		}

		return true
	}

	if packet != nil {
		// If we receive a packet after the end of a flight we need
		// to clear the outgoing list.
		if a.piggyback.newFlight {
			a.piggyback.packets = []packetWithCrc{}
			a.piggyback.packetsIndex = 0
		}
		a.piggyback.newFlight = end
		a.piggyback.queuePacketLocked(packet)
	} else {
		a.piggyback.state = PiggybackingStatePending
	}

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
		return nil, clonePiggybackAcks(a.piggyback.acks)
	}

	packet := a.piggyback.packets[a.piggyback.packetsIndex]
	a.piggyback.packetsIndex = (a.piggyback.packetsIndex + 1) % len(a.piggyback.packets)

	return clonePiggybackPacket(packet.data), clonePiggybackAcks(a.piggyback.acks)
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
	if packet == nil && acks == nil {
		if a.piggyback.state == PiggybackingStatePending && a.piggyback.acks != nil {
			a.log.Infof("Done with the SPED handshake")
			a.piggyback.acks = nil
			a.piggyback.state = PiggybackingStateComplete
			a.piggyback.packets = []packetWithCrc{}
			a.piggyback.packetsIndex = 0
			a.piggyback.newFlight = false
		}
		a.piggyback.mu.Unlock()

		return
	}
	if a.piggyback.state == PiggybackingStateTentative {
		a.piggyback.state = PiggybackingStateConfirmed
	}
	// Handle incoming acks.
	if size := len(acks); size > 0 {
		a.piggyback.packets = slices.DeleteFunc(a.piggyback.packets, func(p packetWithCrc) bool {
			// Remove packets that were acknowledged.
			return slices.Contains(acks, p.crc)
		})
		if len(a.piggyback.packets) == 0 {
			a.piggyback.packetsIndex = 0
		} else if a.piggyback.packetsIndex >= len(a.piggyback.packets) {
			a.piggyback.packetsIndex %= len(a.piggyback.packets)
		}
	}
	if len(packet) == 0 {
		a.piggyback.acks = []uint32{}
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
