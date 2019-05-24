package ice

import "net"

// CandidateServerReflexive ...
type CandidateServerReflexive struct {
	candidateBase
}

// NewCandidateServerReflexive creates a new server reflective candidate
func NewCandidateServerReflexive(network string, ip net.IP, port int, component uint16, relAddr string, relPort int) (*CandidateServerReflexive, error) {
	networkType, err := determineNetworkType(network, ip)
	if err != nil {
		return nil, err
	}

	return &CandidateServerReflexive{
		candidateBase: candidateBase{
			networkType:   networkType,
			candidateType: CandidateTypeServerReflexive,
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
