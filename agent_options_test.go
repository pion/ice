// SPDX-FileCopyrightText: 2025 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
)

func TestDefaultNominationValueGenerator(t *testing.T) {
	t.Run("generates incrementing values", func(t *testing.T) {
		generator := DefaultNominationValueGenerator()

		// Should generate incrementing values starting from 1
		assert.Equal(t, uint32(1), generator())
		assert.Equal(t, uint32(2), generator())
		assert.Equal(t, uint32(3), generator())
	})

	t.Run("each generator has independent counter", func(t *testing.T) {
		gen1 := DefaultNominationValueGenerator()
		gen2 := DefaultNominationValueGenerator()

		assert.Equal(t, uint32(1), gen1())
		assert.Equal(t, uint32(1), gen2()) // Should also start at 1
		assert.Equal(t, uint32(2), gen1())
		assert.Equal(t, uint32(2), gen2())
	})
}

func TestWithRenomination(t *testing.T) {
	t.Run("enables renomination with custom generator", func(t *testing.T) {
		counter := uint32(0)
		customGen := func() uint32 {
			counter++

			return counter * 10
		}

		agent, err := NewAgentWithOptions(WithRenomination(customGen))
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.True(t, agent.enableRenomination)
		assert.NotNil(t, agent.nominationValueGenerator)
		assert.Equal(t, uint32(10), agent.getNominationValue())
		assert.Equal(t, uint32(20), agent.getNominationValue())
	})

	t.Run("enables renomination with default generator", func(t *testing.T) {
		agent, err := NewAgentWithOptions(WithRenomination(DefaultNominationValueGenerator()))
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.True(t, agent.enableRenomination)
		assert.NotNil(t, agent.nominationValueGenerator)
		assert.Equal(t, uint32(1), agent.getNominationValue())
		assert.Equal(t, uint32(2), agent.getNominationValue())
	})

	t.Run("rejects nil generator", func(t *testing.T) {
		_, err := NewAgentWithOptions(WithRenomination(nil))
		assert.ErrorIs(t, err, ErrInvalidNominationValueGenerator)
	})

	t.Run("default agent has renomination disabled", func(t *testing.T) {
		config := &AgentConfig{
			NetworkTypes: []NetworkType{NetworkTypeUDP4},
		}

		agent, err := NewAgent(config)
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.False(t, agent.enableRenomination)
		assert.Nil(t, agent.nominationValueGenerator)
		assert.Equal(t, uint32(0), agent.getNominationValue())
	})
}

func TestWithNominationAttribute(t *testing.T) {
	t.Run("sets custom nomination attribute", func(t *testing.T) {
		agent, err := NewAgentWithOptions(WithNominationAttribute(0x0045))
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.Equal(t, stun.AttrType(0x0045), agent.nominationAttribute)
	})

	t.Run("rejects invalid attribute 0x0000", func(t *testing.T) {
		_, err := NewAgentWithOptions(WithNominationAttribute(0x0000))
		assert.ErrorIs(t, err, ErrInvalidNominationAttribute)
	})

	t.Run("default value when no option", func(t *testing.T) {
		config := &AgentConfig{
			NetworkTypes: []NetworkType{NetworkTypeUDP4},
		}

		agent, err := NewAgent(config)
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		// Should use default value 0x0030
		assert.Equal(t, stun.AttrType(0x0030), agent.nominationAttribute)
	})
}
