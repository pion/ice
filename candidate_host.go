package ice

import (
	"net"
)

// CandidateHost is a candidate of type host
type CandidateHost struct {
	candidateBase
}

// NewCandidateHost creates a new host candidate
func NewCandidateHost(network string, ip net.IP, port int, component uint16) (*CandidateHost, error) {
	networkType, err := determineNetworkType(network, ip)
	if err != nil {
		return nil, err
	}

	return &CandidateHost{
		candidateBase: candidateBase{
			networkType:   networkType,
			candidateType: CandidateTypeHost,
			ip:            ip,
			port:          port,
			component:     component,
		},
	}, nil
}
