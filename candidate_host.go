package ice

import (
	"net"
	"strings"
)

// CandidateHost is a candidate of type host
type CandidateHost struct {
	candidateBase
}

// NewCandidateHost creates a new host candidate
func NewCandidateHost(network string, address string, port int, component uint16) (*CandidateHost, error) {
	c := &CandidateHost{
		candidateBase: candidateBase{
			candidateType: CandidateTypeHost,
			port:          port,
			component:     component,
		},
	}
	if !strings.HasSuffix(address, ".local") {
		ip := net.ParseIP(address)
		if ip == nil {
			return nil, ErrAddressParseFailed
		}

		networkType, err := determineNetworkType(network, ip)
		if err != nil {
			return nil, err
		}

		c.candidateBase.networkType = networkType
		c.candidateBase.resolvedAddr = &net.UDPAddr{IP: ip, Port: port}
	}

	return c, nil
}
