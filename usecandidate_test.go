// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/require"
)

func TestUseCandidateAttr_AddTo(t *testing.T) {
	m := new(stun.Message)
	require.False(t, UseCandidate().IsSet(m))
	require.NoError(t, m.Build(stun.BindingRequest, UseCandidate()))

	m1 := new(stun.Message)
	_, err := m1.Write(m.Raw)
	require.NoError(t, err)
	require.True(t, UseCandidate().IsSet(m1))
}
