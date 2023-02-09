package ice

import (
	"context"
	"errors"
	"fmt"
	"net"
)

var IgnoreCandidateError = errors.New("candidate should be dropped")

type InterceptingGatherer struct {
	Gatherer
	Intercept CandidateFoundHandler
}

func (g *InterceptingGatherer) Gather(ctx context.Context, hdlr CandidateFoundHandler) error {
	return g.Gatherer.Gather(ctx, func(ctx context.Context, cand Candidate, conn net.PacketConn) error {
		if err := g.Intercept(ctx, cand, conn); err != nil {
			if errors.Is(err, IgnoreCandidateError) {
				return nil
			} else {
				return fmt.Errorf("filter failed: %w", err)
			}
		}

		return hdlr(ctx, cand, conn)
	})
}
