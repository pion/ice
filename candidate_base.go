package ice

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/stun"
)

type candidateBase struct {
	networkType   NetworkType
	candidateType CandidateType

	component      uint16
	ip             net.IP
	port           int
	relatedAddress *CandidateRelatedAddress

	lock         sync.RWMutex
	lastSent     time.Time
	lastReceived time.Time

	agent    *Agent
	conn     net.PacketConn
	closeCh  chan struct{}
	closedCh chan struct{}
}

// IP returns Candidate IP
func (c *candidateBase) IP() net.IP {
	return c.ip
}

// Port returns Candidate Port
func (c *candidateBase) Port() int {
	return c.port
}

// Type returns candidate type
func (c *candidateBase) Type() CandidateType {
	return c.candidateType
}

// NetworkType returns candidate NetworkType
func (c *candidateBase) NetworkType() NetworkType {
	return c.networkType
}

// Component returns candidate component
func (c *candidateBase) Component() uint16 {
	return c.component
}

// LocalPreference returns the local preference for this candidate
func (c *candidateBase) LocalPreference() uint16 {
	return defaultLocalPreference
}

// RelatedAddress returns *CandidateRelatedAddress
func (c *candidateBase) RelatedAddress() *CandidateRelatedAddress {
	return c.relatedAddress
}

// start runs the candidate using the provided connection
func (c *candidateBase) start(a *Agent, conn net.PacketConn) {
	c.agent = a
	c.conn = conn
	c.closeCh = make(chan struct{})
	c.closedCh = make(chan struct{})

	go c.recvLoop()
}

func (c *candidateBase) recvLoop() {
	defer func() {
		close(c.closedCh)
	}()

	log := c.agent.log
	buffer := make([]byte, receiveMTU)
	for {
		n, srcAddr, err := c.conn.ReadFrom(buffer)
		if err != nil {
			return
		}

		if stun.IsMessage(buffer[:n]) {
			m := &stun.Message{
				Raw: make([]byte, n),
			}
			// Explicitly copy raw buffer so Message can own the memory.
			copy(m.Raw, buffer[:n])
			if err = m.Decode(); err != nil {
				log.Warnf("Failed to handle decode ICE from %s to %s: %v", c.addr(), srcAddr, err)
				continue
			}
			err = c.agent.run(func(agent *Agent) {
				agent.handleInbound(m, c, srcAddr)
			})
			if err != nil {
				log.Warnf("Failed to handle message: %v", err)
			}

			continue
		}

		isValidRemoteCandidate := make(chan bool, 1)
		err = c.agent.run(func(agent *Agent) {
			isValidRemoteCandidate <- agent.noSTUNSeen(c, srcAddr)
		})

		if err != nil {
			log.Warnf("Failed to handle message: %v", err)
		} else if !<-isValidRemoteCandidate {
			log.Warnf("Discarded message from %s, not a valid remote candidate", c.addr())
		}

		// NOTE This will return packetio.ErrFull if the buffer ever manages to fill up.
		_, err = c.agent.buffer.Write(buffer[:n])
		if err != nil {
			log.Warnf("failed to write packet")
		}
	}
}

// close stops the recvLoop
func (c *candidateBase) close() error {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.conn != nil {
		// Unblock recvLoop
		close(c.closeCh)
		// Close the conn
		err := c.conn.Close()
		if err != nil {
			return err
		}

		// Wait until the recvLoop is closed
		<-c.closedCh
	}

	return nil
}

func (c *candidateBase) writeTo(raw []byte, dst Candidate) (int, error) {
	n, err := c.conn.WriteTo(raw, dst.addr())
	if err != nil {
		return n, fmt.Errorf("failed to send packet: %v", err)
	}
	c.seen(true)
	return n, nil
}

// Priority computes the priority for this ICE Candidate
func (c *candidateBase) Priority() uint32 {
	// The local preference MUST be an integer from 0 (lowest preference) to
	// 65535 (highest preference) inclusive.  When there is only a single IP
	// address, this value SHOULD be set to 65535.  If there are multiple
	// candidates for a particular component for a particular data stream
	// that have the same type, the local preference MUST be unique for each
	// one.
	return (1<<24)*uint32(c.Type().Preference()) +
		(1<<8)*uint32(c.LocalPreference()) +
		uint32(256-c.Component())
}

// Equal is used to compare two candidateBases
func (c *candidateBase) Equal(other Candidate) bool {
	return c.NetworkType() == other.NetworkType() &&
		c.Type() == other.Type() &&
		c.IP().Equal(other.IP()) &&
		c.Port() == other.Port() &&
		c.RelatedAddress().Equal(other.RelatedAddress())
}

// String makes the candidateBase printable
func (c *candidateBase) String() string {
	return fmt.Sprintf("%s %s:%d%s", c.Type(), c.IP(), c.Port(), c.relatedAddress)
}

// LastReceived returns a time.Time indicating the last time
// this candidate was received
func (c *candidateBase) LastReceived() time.Time {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.lastReceived
}

func (c *candidateBase) setLastReceived(t time.Time) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.lastReceived = t
}

// LastSent returns a time.Time indicating the last time
// this candidate was sent
func (c *candidateBase) LastSent() time.Time {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.lastSent
}

func (c *candidateBase) setLastSent(t time.Time) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.lastSent = t
}

func (c *candidateBase) seen(outbound bool) {
	if outbound {
		c.setLastSent(time.Now())
	} else {
		c.setLastReceived(time.Now())
	}
}

func (c *candidateBase) addr() net.Addr {
	return &net.UDPAddr{
		IP:   c.IP(),
		Port: c.Port(),
	}
}
