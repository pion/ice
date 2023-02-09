package ice

import (
	"context"
	"fmt"
	"net"
)

func NewMappingGathererInterceptor(ips []string, candType CandidateType, g Gatherer) (*InterceptingGatherer, error) {
	mapper, err := newExternalIPMapper(candType, ips)
	if err != nil {
		return nil, fmt.Errorf("failed to create IP mapper: %w", err)
	}

	return &InterceptingGatherer{
		Gatherer: g,
		Intercept: func(ctx context.Context, cand Candidate, conn net.PacketConn) error {
			switch c := cand.(type) {
			case *CandidateHost:
				if mapper.candidateType == CandidateTypeHost {
					if mappedIP, err := mapper.findExternalIP(c.Address()); err == nil {
						c.setIP(mappedIP)
					}
				}

			case *CandidateServerReflexive:
				if mapper.candidateType == CandidateTypeServerReflexive {
					if _ /*mappedIP*/, err := mapper.findExternalIP(c.Address()); err == nil {
						// c.setIP(mappedIP) // TODO
					}
				}
			}

			return nil
		},
	}, nil
}
