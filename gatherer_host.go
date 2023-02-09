package ice

import "context"

type (
	HostUDPGatherer struct{}

	HostPassiveTCPGatherer struct{}

	HostUDPMuxGatherer struct {
		mux UDPMux
	}
)

func NewHostGatherer(p *GatherParams) MultiGatherer {
	mg := MultiGatherer{}

	for _, nt := range p.NetworkTypes {
		switch {
		case nt.IsUDP():
			if p.UDPMux == nil {
				mg = append(mg, &HostUDPGatherer{})
			} else {
				mg = append(mg, &HostUDPMuxGatherer{})
			}

		case nt.IsTCP():
			mg = append(mg, &HostPassiveTCPGatherer{})
		}
	}

	return mg
}

func (g *HostUDPGatherer) Gather(ctx context.Context, hdlr CandidateFoundHandler) error {
	return nil
}

func (g *HostPassiveTCPGatherer) Gather(ctx context.Context, hdlr CandidateFoundHandler) error {
	return nil
}

func (g *HostUDPMuxGatherer) Gather(ctx context.Context, hdlr CandidateFoundHandler) error {
	return nil
}
