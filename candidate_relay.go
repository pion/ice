package ice

import (
	"net"
)

// CandidateRelay ...
type CandidateRelay struct {
	candidateBase
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
	}, nil
}
