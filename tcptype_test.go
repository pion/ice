// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTCPType(t *testing.T) {
	assert := assert.New(t)

	var tcpType TCPType

	assert.Equal(TCPTypeUnspecified, tcpType)
	assert.Equal(TCPTypeActive, NewTCPType("active"))
	assert.Equal(TCPTypePassive, NewTCPType("passive"))
	assert.Equal(TCPTypeSimultaneousOpen, NewTCPType("so"))
	assert.Equal(TCPTypeUnspecified, NewTCPType("something else"))

	assert.Empty(TCPTypeUnspecified.String())
	assert.Equal("active", TCPTypeActive.String())
	assert.Equal("passive", TCPTypePassive.String())
	assert.Equal("so", TCPTypeSimultaneousOpen.String())
	assert.Equal("Unknown", TCPType(-1).String())
}
