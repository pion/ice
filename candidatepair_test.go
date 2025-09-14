// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func hostCandidate() *CandidateHost {
	return &CandidateHost{
		candidateBase: candidateBase{
			candidateType: CandidateTypeHost,
			component:     ComponentRTP,
		},
	}
}

func prflxCandidate() *CandidatePeerReflexive {
	return &CandidatePeerReflexive{
		candidateBase: candidateBase{
			candidateType: CandidateTypePeerReflexive,
			component:     ComponentRTP,
		},
	}
}

func srflxCandidate() *CandidateServerReflexive {
	return &CandidateServerReflexive{
		candidateBase: candidateBase{
			candidateType: CandidateTypeServerReflexive,
			component:     ComponentRTP,
		},
	}
}

func relayCandidate() *CandidateRelay {
	return &CandidateRelay{
		candidateBase: candidateBase{
			candidateType: CandidateTypeRelay,
			component:     ComponentRTP,
		},
	}
}

func TestCandidatePairPriority(t *testing.T) {
	for _, test := range []struct {
		Pair         *CandidatePair
		WantPriority uint64
	}{
		{
			Pair: newCandidatePair(
				hostCandidate(),
				hostCandidate(),
				false,
			),
			WantPriority: 9151314440652587007,
		},
		{
			Pair: newCandidatePair(
				hostCandidate(),
				hostCandidate(),
				true,
			),
			WantPriority: 9151314440652587007,
		},
		{
			Pair: newCandidatePair(
				hostCandidate(),
				prflxCandidate(),
				true,
			),
			WantPriority: 7998392936314175488,
		},
		{
			Pair: newCandidatePair(
				hostCandidate(),
				prflxCandidate(),
				false,
			),
			WantPriority: 7998392936314175487,
		},
		{
			Pair: newCandidatePair(
				hostCandidate(),
				srflxCandidate(),
				true,
			),
			WantPriority: 7277816996102668288,
		},
		{
			Pair: newCandidatePair(
				hostCandidate(),
				srflxCandidate(),
				false,
			),
			WantPriority: 7277816996102668287,
		},
		{
			Pair: newCandidatePair(
				hostCandidate(),
				relayCandidate(),
				true,
			),
			WantPriority: 72057593987596288,
		},
		{
			Pair: newCandidatePair(
				hostCandidate(),
				relayCandidate(),
				false,
			),
			WantPriority: 72057593987596287,
		},
	} {
		require.Equal(t, test.Pair.priority(), test.WantPriority)
	}
}

func TestCandidatePairEquality(t *testing.T) {
	pairA := newCandidatePair(hostCandidate(), srflxCandidate(), true)
	pairB := newCandidatePair(hostCandidate(), srflxCandidate(), false)

	require.True(t, pairA.equal(pairB))
}

func TestNilCandidatePairString(t *testing.T) {
	var nilCandidatePair *CandidatePair
	require.Equal(t, nilCandidatePair.String(), "")
}

func TestCandidatePairState_String(t *testing.T) {
	tests := []struct {
		name string
		in   CandidatePairState
		want string
	}{
		{"waiting", CandidatePairStateWaiting, "waiting"},
		{"in-progress", CandidatePairStateInProgress, "in-progress"},
		{"failed", CandidatePairStateFailed, "failed"},
		{"succeeded", CandidatePairStateSucceeded, "succeeded"},
		{"unknown", CandidatePairState(255), "Unknown candidate pair state"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.in.String())
		})
	}
}

func TestCandidatePairEqual_NilCases(t *testing.T) {
	// both nil -> true
	var a *CandidatePair
	var b *CandidatePair
	require.True(t, a.equal(b), "both nil pairs should be equal")

	// left non-nil, right nil -> false
	a = newCandidatePair(hostCandidate(), srflxCandidate(), true)
	require.False(t, a.equal(nil), "non-nil vs nil should be false")

	// left nil, right non-nil -> false
	require.False(t, (*CandidatePair)(nil).equal(a), "nil vs non-nil should be false")
}

func TestCandidatePair_TimeGetters_DefaultZero(t *testing.T) {
	p := newCandidatePair(hostCandidate(), srflxCandidate(), true)

	require.True(t, p.FirstRequestSentAt().IsZero(), "FirstRequestSentAt should be zero by default")
	require.True(t, p.LastRequestSentAt().IsZero(), "LastRequestSentAt should be zero by default")
	require.True(t, p.FirstReponseReceivedAt().IsZero(), "FirstReponseReceivedAt should be zero by default")
	require.True(t, p.LastResponseReceivedAt().IsZero(), "LastResponseReceivedAt should be zero by default")
	require.True(t, p.FirstRequestReceivedAt().IsZero(), "FirstRequestReceivedAt should be zero by default")
	require.True(t, p.LastRequestReceivedAt().IsZero(), "LastRequestReceivedAt should be zero by default")
}
