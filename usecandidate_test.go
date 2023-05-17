// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"

	"github.com/pion/stun"
	"github.com/stretchr/testify/assert"
)

func TestUseCandidateAttr_AddTo(t *testing.T) {
	assert := assert.New(t)

	m := new(stun.Message)
	assert.False(UseCandidate().IsSet(m), "UseCandidate attribute should not be set")

	err := m.Build(stun.BindingRequest, UseCandidate())
	assert.NoError(err)

	m1 := new(stun.Message)

	_, err = m1.Write(m.Raw)
	assert.NoError(err)

	assert.True(UseCandidate().IsSet(m1), "UseCandidate attribute should be set")
}
