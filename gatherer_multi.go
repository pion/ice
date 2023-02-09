package ice

import (
	"context"

	"golang.org/x/sync/errgroup"
)

type MultiGatherer = ParallelGatherer

type ParallelGatherer []Gatherer

func (pg ParallelGatherer) Gather(ctx context.Context, hdlr CandidateFoundHandler) error {
	grp := &errgroup.Group{}

	for _, g := range pg {
		g := g

		grp.Go(func() error {
			return g.Gather(ctx, hdlr)
		})
	}
	return grp.Wait()
}

type SerialGatherer []Gatherer

func (sg SerialGatherer) Gather(ctx context.Context, hdlr CandidateFoundHandler) error {
	for _, g := range sg {
		if err := g.Gather(ctx, hdlr); err != nil {
			return err
		}
	}

	return nil
}
