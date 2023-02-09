package ice

import (
	"context"
	"net"
)

type StaticGatherer struct {
	Candidates map[Candidate]net.PacketConn
}

func (g *StaticGatherer) Gather(ctx context.Context, hdlr CandidateFoundHandler) error {
	for cand, conn := range g.Candidates {
		hdlr(ctx, cand, conn)
	}

	return nil
}
