// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/require"
)

func TestPriority_GetFrom(t *testing.T) { //nolint:dupl
	m := new(stun.Message)
	var priority PriorityAttr
	require.ErrorIs(t, stun.ErrAttributeNotFound, priority.GetFrom(m))
	require.NoError(t, m.Build(stun.BindingRequest, &priority))

	m1 := new(stun.Message)
	_, err := m1.Write(m.Raw)
	require.NoError(t, err)

	var p1 PriorityAttr
	require.NoError(t, p1.GetFrom(m1))
	require.Equal(t, p1, priority)
	t.Run("IncorrectSize", func(t *testing.T) {
		m3 := new(stun.Message)
		m3.Add(stun.AttrPriority, make([]byte, 100))
		var p2 PriorityAttr
		require.True(t, stun.IsAttrSizeInvalid(p2.GetFrom(m3)))
	})
}
