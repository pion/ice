// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"context"
	"testing"

	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5/test"
	"github.com/stretchr/testify/require"
)

func TestAgentUpdateOptionsConcurrentWithGathering(t *testing.T) {
	defer test.CheckRoutines(t)()

	firstURLs := []*stun.URI{{Scheme: stun.SchemeTypeSTUN, Host: "127.0.0.1", Port: 9, Proto: stun.ProtoTypeUDP}}
	secondURLs := []*stun.URI{{Scheme: stun.SchemeTypeSTUN, Host: "127.0.0.1", Port: 10, Proto: stun.ProtoTypeUDP}}
	agent, err := NewAgentWithOptions(
		WithUrls(firstURLs),
		WithCandidateTypes([]CandidateType{CandidateTypeServerReflexive}),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
	)
	require.NoError(t, err)
	require.NoError(t, agent.OnCandidate(func(Candidate) {}))

	updatesDone := make(chan error, 1)
	go func() {
		for index := range 256 {
			urls := firstURLs
			if index%2 == 0 {
				urls = secondURLs
			}
			if updateErr := agent.UpdateOptions(WithUrls(urls)); updateErr != nil {
				updatesDone <- updateErr

				return
			}
		}
		updatesDone <- nil
	}()
	for range 256 {
		require.NoError(t, agent.Restart("", ""))
		require.NoError(t, agent.GatherCandidates())
	}
	require.NoError(t, <-updatesDone)
	require.NoError(t, agent.Close())
}

func TestAgentSnapshotURLs(t *testing.T) {
	defer test.CheckRoutines(t)()

	firstURLs := []*stun.URI{{Scheme: stun.SchemeTypeSTUN, Host: "127.0.0.1", Port: 9, Proto: stun.ProtoTypeUDP}}
	secondURLs := []*stun.URI{{Scheme: stun.SchemeTypeSTUN, Host: "127.0.0.1", Port: 10, Proto: stun.ProtoTypeUDP}}
	agent, err := NewAgentWithOptions(
		WithUrls(firstURLs),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
	)
	require.NoError(t, err)

	urls, err := agent.snapshotURLs(context.Background())
	require.NoError(t, err)
	require.Equal(t, firstURLs, urls)
	urls[0] = secondURLs[0]
	urls, err = agent.snapshotURLs(context.Background())
	require.NoError(t, err)
	require.Equal(t, firstURLs, urls)

	require.NoError(t, agent.UpdateOptions(WithUrls(secondURLs)))
	urls, err = agent.snapshotURLs(context.Background())
	require.NoError(t, err)
	require.Equal(t, secondURLs, urls)

	require.NoError(t, agent.Close())
	_, err = agent.snapshotURLs(context.Background())
	require.Error(t, err)
}

func TestURLUpdateDuringGathering(t *testing.T) {
	for _, candidateType := range []CandidateType{CandidateTypeRelay, CandidateTypeServerReflexive} {
		t.Run(candidateType.String(), func(t *testing.T) {
			agent, err := NewAgentWithOptions(
				WithCandidateTypes([]CandidateType{candidateType}),
				WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
				WithMulticastDNSMode(MulticastDNSModeDisabled),
			)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, agent.Close())
			})
			require.NoError(t, agent.OnCandidate(func(Candidate) {}))
			done := make(chan error, 1)
			go func() {
				for range 1024 {
					if err := agent.UpdateOptions(WithUrls(nil)); err != nil {
						done <- err

						return
					}
				}
				done <- nil
			}()
			for range 128 {
				require.NoError(t, agent.Restart("", ""))
				require.NoError(t, agent.GatherCandidates())
			}
			require.NoError(t, <-done)
		})
	}
}
