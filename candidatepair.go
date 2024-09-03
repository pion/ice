// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"fmt"
	"sync/atomic"

	"github.com/pion/stun/v3"
)

func newCandidatePair(local, remote Candidate, controlling bool) *CandidatePair {
	return &CandidatePair{
		iceRoleControlling: controlling,
		Remote:             remote,
		Local:              local,
		state:              CandidatePairStateWaiting,
	}
}

// CandidatePair is a combination of a
// local and remote candidate
type CandidatePair struct {
	bytesReceived            uint64
	bytesSent                uint64
	packetsReceived          uint32
	packetsSent              uint32
	iceRoleControlling       bool
	Remote                   Candidate
	Local                    Candidate
	bindingRequestCount      uint16
	state                    CandidatePairState
	nominated                bool
	nominateOnBindingSuccess bool
}

func (p *CandidatePair) String() string {
	if p == nil {
		return ""
	}

	return fmt.Sprintf("prio %d (local, prio %d) %s <-> %s (remote, prio %d), state: %s, nominated: %v, nominateOnBindingSuccess: %v",
		p.priority(), p.Local.Priority(), p.Local, p.Remote, p.Remote.Priority(), p.state, p.nominated, p.nominateOnBindingSuccess)
}

func (p *CandidatePair) equal(other *CandidatePair) bool {
	if p == nil && other == nil {
		return true
	}
	if p == nil || other == nil {
		return false
	}
	return p.Local.Equal(other.Local) && p.Remote.Equal(other.Remote)
}

// RFC 5245 - 5.7.2.  Computing Pair Priority and Ordering Pairs
// Let G be the priority for the candidate provided by the controlling
// agent.  Let D be the priority for the candidate provided by the
// controlled agent.
// pair priority = 2^32*MIN(G,D) + 2*MAX(G,D) + (G>D?1:0)
func (p *CandidatePair) priority() uint64 {
	var g, d uint32
	if p.iceRoleControlling {
		g = p.Local.Priority()
		d = p.Remote.Priority()
	} else {
		g = p.Remote.Priority()
		d = p.Local.Priority()
	}

	// Just implement these here rather
	// than fooling around with the math package
	localMin := func(x, y uint32) uint64 {
		if x < y {
			return uint64(x)
		}
		return uint64(y)
	}
	localMax := func(x, y uint32) uint64 {
		if x > y {
			return uint64(x)
		}
		return uint64(y)
	}
	cmp := func(x, y uint32) uint64 {
		if x > y {
			return uint64(1)
		}
		return uint64(0)
	}

	// 1<<32 overflows uint32; and if both g && d are
	// maxUint32, this result would overflow uint64
	return (1<<32-1)*localMin(g, d) + 2*localMax(g, d) + cmp(g, d)
}

// BytesSent returns the number of bytes sent
func (p *CandidatePair) BytesSent() uint64 {
	return atomic.LoadUint64(&p.bytesSent)
}

// BytesReceived returns the number of bytes received
func (p *CandidatePair) BytesReceived() uint64 {
	return atomic.LoadUint64(&p.bytesReceived)
}

// PacketsSent returns the number of bytes sent
func (p *CandidatePair) PacketsSent() uint32 {
	return atomic.LoadUint32(&p.packetsSent)
}

// PacketsReceived returns the number of bytes received
func (p *CandidatePair) PacketsReceived() uint32 {
	return atomic.LoadUint32(&p.packetsReceived)
}

func (p *CandidatePair) Write(b []byte) (int, error) {
	atomic.AddUint64(&p.bytesSent, uint64(len(b)))
	return p.Local.writeTo(b, p.Remote)
}

func (a *Agent) sendSTUN(msg *stun.Message, local, remote Candidate) {
	_, err := local.writeTo(msg.Raw, remote)
	if err != nil {
		a.log.Tracef("Failed to send STUN message: %s", err)
	}
}
