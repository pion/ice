package ice

import "context"

type ServerReflexiveGatherer struct{}

func NewServerReflexiveGatherer(p *GatherParams) *ServerReflexiveGatherer {
	return &ServerReflexiveGatherer{}
}

func (g *ServerReflexiveGatherer) Gather(ctx context.Context, hdlr CandidateFoundHandler) error {
	return nil
}
