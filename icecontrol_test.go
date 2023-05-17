// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"

	"github.com/pion/stun"
	"github.com/stretchr/testify/assert"
)

func TestControlled_GetFrom(t *testing.T) { //nolint:dupl
	assert := assert.New(t)

	m := new(stun.Message)
	var c AttrControlled
	err := c.GetFrom(m)
	assert.ErrorIs(err, stun.ErrAttributeNotFound)

	err = m.Build(stun.BindingRequest, &c)
	assert.NoError(err)

	m1 := new(stun.Message)
	_, err = m1.Write(m.Raw)
	assert.NoError(err)

	var c1 AttrControlled
	err = c1.GetFrom(m1)
	assert.NoError(err)

	assert.Equal(c1, c)

	t.Run("IncorrectSize", func(t *testing.T) {
		m3 := new(stun.Message)
		m3.Add(stun.AttrICEControlled, make([]byte, 100))
		var c2 AttrControlled
		err := c2.GetFrom(m3)
		assert.True(stun.IsAttrSizeInvalid(err))
	})
}

func TestControlling_GetFrom(t *testing.T) { //nolint:dupl
	a := assert.New(t)

	m := new(stun.Message)
	var c AttrControlling
	err := c.GetFrom(m)
	a.ErrorIs(err, stun.ErrAttributeNotFound)

	err = m.Build(stun.BindingRequest, &c)
	a.NoError(err)

	m1 := new(stun.Message)
	_, err = m1.Write(m.Raw)
	a.NoError(err)

	var c1 AttrControlling
	err = c1.GetFrom(m1)
	a.NoError(err)

	a.Equal(c1, c)

	t.Run("IncorrectSize", func(t *testing.T) {
		a := assert.New(t)

		m3 := new(stun.Message)
		m3.Add(stun.AttrICEControlling, make([]byte, 100))
		var c2 AttrControlling
		err := c2.GetFrom(m3)
		a.True(stun.IsAttrSizeInvalid(err), "should error")
	})
}

func TestControl_GetFrom(t *testing.T) {
	t.Run("Blank", func(t *testing.T) {
		assert := assert.New(t)

		m := new(stun.Message)
		var c AttrControl
		err := c.GetFrom(m)
		assert.ErrorIs(err, stun.ErrAttributeNotFound)
	})

	t.Run("Controlling", func(t *testing.T) { //nolint:dupl
		a := assert.New(t)

		m := new(stun.Message)
		var c AttrControl
		err := c.GetFrom(m)
		a.ErrorIs(err, stun.ErrAttributeNotFound)

		c.Role = Controlling
		c.Tiebreaker = 4321
		err = m.Build(stun.BindingRequest, &c)
		a.NoError(err)

		m1 := new(stun.Message)
		_, err = m1.Write(m.Raw)
		a.NoError(err)

		var c1 AttrControl
		err = c1.GetFrom(m1)
		a.NoError(err)

		a.Equal(c1, c)

		t.Run("IncorrectSize", func(t *testing.T) {
			a := assert.New(t)

			m3 := new(stun.Message)
			m3.Add(stun.AttrICEControlling, make([]byte, 100))
			var c2 AttrControl
			err := c2.GetFrom(m3)
			a.True(stun.IsAttrSizeInvalid(err), "should error")
		})
	})
	t.Run("Controlled", func(t *testing.T) { //nolint:dupl
		a := assert.New(t)

		m := new(stun.Message)
		var c AttrControl
		err := c.GetFrom(m)
		a.ErrorIs(err, stun.ErrAttributeNotFound)

		c.Role = Controlled
		c.Tiebreaker = 1234
		err = m.Build(stun.BindingRequest, &c)
		a.NoError(err)

		m1 := new(stun.Message)
		_, err = m1.Write(m.Raw)
		a.NoError(err)

		var c1 AttrControl
		err = c1.GetFrom(m1)
		a.NoError(err)

		a.Equal(c1, c)

		t.Run("IncorrectSize", func(t *testing.T) {
			a := assert.New(t)

			m3 := new(stun.Message)
			m3.Add(stun.AttrICEControlling, make([]byte, 100))
			var c2 AttrControl
			err := c2.GetFrom(m3)
			a.True(stun.IsAttrSizeInvalid(err), "should error")
		})
	})
}
