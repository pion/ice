// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCandidateType_String_KnownCases(t *testing.T) {
	cases := map[CandidateType]string{
		CandidateTypeHost:            "host",
		CandidateTypeServerReflexive: "srflx",
		CandidateTypePeerReflexive:   "prflx",
		CandidateTypeRelay:           "relay",
		CandidateTypeUnspecified:     "Unknown candidate type",
	}

	for ct, want := range cases {
		require.Equal(t, want, ct.String(), "unexpected string for %v", ct)
	}
}

func TestCandidateType_String_Default(t *testing.T) {
	const outOfBounds CandidateType = 255
	require.Equal(t, "Unknown candidate type", outOfBounds.String())
}

func TestCandidateType_Preference_DefaultCase(t *testing.T) {
	const outOfBounds CandidateType = 255
	require.Equal(t, uint16(0), outOfBounds.Preference())
}

func TestContainsCandidateType_NilSlice(t *testing.T) {
	var list []CandidateType // nil slice
	require.False(t, containsCandidateType(CandidateTypeHost, list))
}
