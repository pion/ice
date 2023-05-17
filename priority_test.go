// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"

	"github.com/pion/stun"
	"github.com/stretchr/testify/assert"
)

func TestPriority_GetFrom(t *testing.T) { //nolint:dupl
	a := assert.New(t)

	m := new(stun.Message)
	var p PriorityAttr
	err := p.GetFrom(m)
	a.ErrorIs(err, stun.ErrAttributeNotFound)

	err = m.Build(stun.BindingRequest, &p)
	a.NoError(err)

	m1 := new(stun.Message)
	_, err = m1.Write(m.Raw)
	a.NoError(err)

	var p1 PriorityAttr
	err = p1.GetFrom(m1)
	a.NoError(err)

	a.Equal(p1, p)

	t.Run("IncorrectSize", func(t *testing.T) {
		a := assert.New(t)

		m3 := new(stun.Message)
		m3.Add(stun.AttrPriority, make([]byte, 100))
		var p2 PriorityAttr
		err := p2.GetFrom(m3)
		a.True(stun.IsAttrSizeInvalid(err))
	})
}
