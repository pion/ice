// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"context"
	"net"
	"net/netip"
	"time"
)

const (
	receiveMTU             = 8192
	defaultLocalPreference = 65535

	// ComponentRTP indicates that the candidate is used for RTP.
	ComponentRTP uint16 = 1
	// ComponentRTCP indicates that the candidate is used for RTCP.
	ComponentRTCP
)

// Candidate represents an ICE candidate.
type Candidate interface {
	// An arbitrary string used in the freezing algorithm to
	// group similar candidates.  It is the same for two candidates that
	// have the same type, base IP address, protocol (UDP, TCP, etc.),
	// and STUN or TURN server.
	Foundation() string

	// ID is a unique identifier for just this candidate
	// Unlike the foundation this is different for each candidate
	ID() string

	// A component is a piece of a data stream.
	// An example is one for RTP, and one for RTCP
	Component() uint16
	SetComponent(uint16)

	// The last time this candidate received traffic
	LastReceived() time.Time

	// The last time this candidate sent traffic
	LastSent() time.Time

	NetworkType() NetworkType
	Address() string
	Port() int

	Priority() uint32

	// Extensions returns a copy of all extension attributes associated with the ICECandidate.
	// In the order of insertion, *(key value).
	// Extension attributes are defined in RFC 5245, Section 15.1:
	// https://datatracker.ietf.org/doc/html/rfc5245#section-15.1
	//.
	Extensions() []CandidateExtension
	// SetExtensions replaces all extensions with a validated copy.
	// It preserves order and duplicates and leaves the candidate unchanged on error.
	SetExtensions([]CandidateExtension) error
	// GetExtension returns the value of the extension attribute associated with the ICECandidate.
	// Extension attributes are defined in RFC 5245, Section 15.1:
	// https://datatracker.ietf.org/doc/html/rfc5245#section-15.1
	//.
	GetExtension(key string) (value CandidateExtension, ok bool)
	// AddExtension adds an extension attribute to the ICECandidate.
	// If an extension with the same key already exists, the first is overwritten.
	// Extension attributes are defined in RFC 5245, Section 15.1:
	AddExtension(extension CandidateExtension) error
	// RemoveExtension removes the first extension attribute with the given key.
	// Extension attributes are defined in RFC 5245, Section 15.1:
	RemoveExtension(key string) (ok bool)

	String() string
	Type() CandidateType

	// TCPType returns the TCP mode extension, or TCPTypeUnspecified if absent or unrecognized.
	TCPType() TCPType

	// Equal compares the transport address (including TCP type) and candidate type.
	// Use DeepEqual to also compare extensions.
	Equal(other Candidate) bool

	// DeepEqual same as Equal, But it also compares the candidate extensions.
	DeepEqual(other Candidate) bool

	Marshal() string

	// transportAddressEqual checks if the transport address (IP, Port, NetworkType, TCPType) is equal to another
	// candidate.
	transportAddressEqual(other Candidate) bool

	addr() net.Addr
	addrPort() netip.AddrPort
	canWriteTo(Candidate) bool
	filterForLocationTracking() bool
	agent() *Agent
	context() context.Context

	close() error
	copy() (Candidate, error)
	seen(outbound bool)
	start(a *Agent, conn net.PacketConn, initializedCh <-chan struct{})
	writeTo(raw []byte, dst Candidate) (int, error)
	setPriority(uint32)
	setIPAddr(addr netip.Addr) error

	replaceRemoteCandidateCacheValues(oldRemote, newRemote Candidate)
}
