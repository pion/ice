package ice

import "net"

// CandidatePeerReflexive ...
type CandidatePeerReflexive struct {
	candidateBase
}

// NewCandidatePeerReflexive creates a new peer reflective candidate
func NewCandidatePeerReflexive(network string, ip net.IP, port int, component uint16, relAddr string, relPort int) (*CandidatePeerReflexive, error) {
	networkType, err := determineNetworkType(network, ip)
	if err != nil {
		return nil, err
	}

	return &CandidatePeerReflexive{
		candidateBase: candidateBase{
			networkType:   networkType,
			candidateType: CandidateTypePeerReflexive,
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
