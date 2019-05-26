package ice

import (
	"errors"
	"net"

	"github.com/pion/turnc"
)

// CandidateRelay ...
type CandidateRelay struct {
	candidateBase

	allocation  *turnc.Allocation
	permissions map[string]*turnc.Permission
}

// NewCandidateRelay creates a new relay candidate
func NewCandidateRelay(network string, ip net.IP, port int, component uint16, relAddr string, relPort int) (*CandidateRelay, error) {
	networkType, err := determineNetworkType(network, ip)
	if err != nil {
		return nil, err
	}

	return &CandidateRelay{
		candidateBase: candidateBase{
			networkType:   networkType,
			candidateType: CandidateTypeRelay,
			ip:            ip,
			port:          port,
			component:     component,
			relatedAddress: &CandidateRelatedAddress{
				Address: relAddr,
				Port:    relPort,
			},
		},
		permissions: map[string]*turnc.Permission{},
	}, nil
}

func (c *CandidateRelay) setAllocation(a *turnc.Allocation) {
	c.allocation = a
}

func (c *CandidateRelay) start(a *Agent, conn net.PacketConn) {
	c.currAgent = a
}

func (c *CandidateRelay) close() error {
	return nil
}

func (c *CandidateRelay) addPermission(dst Candidate) error {
	permission, err := c.allocation.Create(dst.addr())
	if err != nil {
		return err
	}

	c.lock.Lock()
	c.permissions[dst.String()] = permission
	c.lock.Unlock()

	go func(remoteAddr net.Addr) {
		log := c.agent().log
		buffer := make([]byte, receiveMTU)
		for {
			n, err := permission.Read(buffer)
			if err != nil {
				return
			}

			handleInboundCandidateMsg(c, buffer[:n], remoteAddr, log)
		}
	}(dst.addr())
	return nil
}

func (c *CandidateRelay) writeTo(raw []byte, dst Candidate) (int, error) {
	permission, ok := c.permissions[dst.String()]
	if !ok {
		return 0, errors.New("no permission created for remote candidate")
	}

	return permission.Write(raw)
}
