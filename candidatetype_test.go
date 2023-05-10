// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCandidateTypePreference(t *testing.T) {
	r := require.New(t)

	hostDefaultPreference := uint16(126)
	prflxDefaultPreference := uint16(110)
	srflxDefaultPreference := uint16(100)
	relayDefaultPreference := uint16(0)
	unspecifiedDefaultPreference := uint16(0)

	tcpOffsets := []uint16{0, 10}

	for _, tcpOffset := range tcpOffsets {
		for _, networkType := range supportedNetworkTypes() {
			if networkType.IsTCP() {
				r.Equal(hostDefaultPreference-tcpOffset, CandidateTypeHost.Preference(networkType, tcpOffset))
				r.Equal(prflxDefaultPreference-tcpOffset, CandidateTypePeerReflexive.Preference(networkType, tcpOffset))
				r.Equal(srflxDefaultPreference-tcpOffset, CandidateTypeServerReflexive.Preference(networkType, tcpOffset))
			} else {
				r.Equal(hostDefaultPreference, CandidateTypeHost.Preference(networkType, tcpOffset))
				r.Equal(prflxDefaultPreference, CandidateTypePeerReflexive.Preference(networkType, tcpOffset))
				r.Equal(srflxDefaultPreference, CandidateTypeServerReflexive.Preference(networkType, tcpOffset))
			}
		}
	}
	for _, tcpOffset := range tcpOffsets {
		for _, networkType := range supportedNetworkTypes() {
			r.Equal(relayDefaultPreference, CandidateTypeRelay.Preference(networkType, tcpOffset))
			r.Equal(unspecifiedDefaultPreference, CandidateTypeUnspecified.Preference(networkType, tcpOffset))
		}
	}
}
