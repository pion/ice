package ice

import (
	"context"

	"github.com/pion/stun"
)

type RelayGatherer struct {
	URI *stun.URI
}

func NewRelayGatherers(p *GatherParams) MultiGatherer {
	mg := MultiGatherer{}

	for _, uri := range p.URIs {
		mg = append(mg, &RelayGatherer{
			URI: uri,
		})
	}

	return mg
}

func (g *RelayGatherer) Gather(ctx context.Context, hdlr CandidateFoundHandler) error {
	return nil
}
