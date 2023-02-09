package ice

import "context"

type MulticastDNSGatherer struct{}

func NewMulticastDNSGatherer(p *GatherParams) *MulticastDNSGatherer {
	return &MulticastDNSGatherer{}
}

func (g *MulticastDNSGatherer) Gather(ctx context.Context, hdlr CandidateFoundHandler) error {
	return nil
}
