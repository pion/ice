package ice

import (
	"net"
	"time"
)

const (
	receiveMTU             = 8192
	defaultLocalPreference = 65535

	// ComponentRTP indicates that the candidate is used for RTP
	ComponentRTP uint16 = 1
	// ComponentRTCP indicates that the candidate is used for RTCP
	ComponentRTCP
)

// Candidate represents an ICE candidate
type Candidate interface {
	start(a *Agent, conn net.PacketConn)
	addr() net.Addr

	setLastSent(t time.Time)
	seen(outbound bool)
	LastSent() time.Time
	setLastReceived(t time.Time)
	LastReceived() time.Time
	String() string
	Equal(other Candidate) bool
	Priority() uint32
	writeTo(raw []byte, dst Candidate) (int, error)
	close() error

	IP() net.IP
	Port() int
	Component() uint16
	NetworkType() NetworkType

	Type() CandidateType
	RelatedAddress() *CandidateRelatedAddress
}
