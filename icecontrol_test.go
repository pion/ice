// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/require"
)

func TestControlled_GetFrom(t *testing.T) { //nolint:dupl
	m := new(stun.Message)
	var attrCtr AttrControlled
	require.ErrorIs(t, stun.ErrAttributeNotFound, attrCtr.GetFrom(m))
	require.NoError(t, m.Build(stun.BindingRequest, &attrCtr))

	m1 := new(stun.Message)
	_, err := m1.Write(m.Raw)
	require.NoError(t, err)

	var c1 AttrControlled
	require.NoError(t, c1.GetFrom(m1))
	require.Equal(t, c1, attrCtr)

	t.Run("IncorrectSize", func(t *testing.T) {
		m3 := new(stun.Message)
		m3.Add(stun.AttrICEControlled, make([]byte, 100))
		var c2 AttrControlled
		require.True(t, stun.IsAttrSizeInvalid(c2.GetFrom(m3)))
	})
}

func TestControlling_GetFrom(t *testing.T) { //nolint:dupl
	m := new(stun.Message)
	var attrCtr AttrControlling
	require.ErrorIs(t, stun.ErrAttributeNotFound, attrCtr.GetFrom(m))
	require.NoError(t, m.Build(stun.BindingRequest, &attrCtr))

	m1 := new(stun.Message)
	_, err := m1.Write(m.Raw)
	require.NoError(t, err)

	var c1 AttrControlling
	require.NoError(t, c1.GetFrom(m1))
	require.Equal(t, c1, attrCtr)
	t.Run("IncorrectSize", func(t *testing.T) {
		m3 := new(stun.Message)
		m3.Add(stun.AttrICEControlling, make([]byte, 100))
		var c2 AttrControlling
		require.True(t, stun.IsAttrSizeInvalid(c2.GetFrom(m3)))
	})
}

func TestControl_GetFrom(t *testing.T) { //nolint:cyclop
	t.Run("Blank", func(t *testing.T) {
		m := new(stun.Message)
		var c AttrControl
		require.ErrorIs(t, stun.ErrAttributeNotFound, c.GetFrom(m))
	})
	t.Run("Controlling", func(t *testing.T) { //nolint:dupl
		m := new(stun.Message)
		var attCtr AttrControl
		require.ErrorIs(t, stun.ErrAttributeNotFound, attCtr.GetFrom(m))
		attCtr.Role = Controlling
		attCtr.Tiebreaker = 4321
		require.NoError(t, m.Build(stun.BindingRequest, &attCtr))
		m1 := new(stun.Message)
		_, err := m1.Write(m.Raw)
		require.NoError(t, err)
		var c1 AttrControl
		require.NoError(t, c1.GetFrom(m1))
		require.Equal(t, c1, attCtr)
		t.Run("IncorrectSize", func(t *testing.T) {
			m3 := new(stun.Message)
			m3.Add(stun.AttrICEControlling, make([]byte, 100))
			var c2 AttrControl
			err := c2.GetFrom(m3)
			require.True(t, stun.IsAttrSizeInvalid(err))
		})
	})
	t.Run("Controlled", func(t *testing.T) { //nolint:dupl
		m := new(stun.Message)
		var attrCtrl AttrControl
		require.ErrorIs(t, stun.ErrAttributeNotFound, attrCtrl.GetFrom(m))
		attrCtrl.Role = Controlled
		attrCtrl.Tiebreaker = 1234
		require.NoError(t, m.Build(stun.BindingRequest, &attrCtrl))
		m1 := new(stun.Message)
		_, err := m1.Write(m.Raw)
		require.NoError(t, err)

		var c1 AttrControl
		require.NoError(t, c1.GetFrom(m1))
		require.Equal(t, c1, attrCtrl)
		t.Run("IncorrectSize", func(t *testing.T) {
			m3 := new(stun.Message)
			m3.Add(stun.AttrICEControlling, make([]byte, 100))
			var c2 AttrControl
			err := c2.GetFrom(m3)
			require.True(t, stun.IsAttrSizeInvalid(err))
		})
	})
}
