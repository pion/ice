// SPDX-FileCopyrightText: 2025 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"sync/atomic"

	"github.com/pion/stun/v3"
)

// AgentOption represents a function that can be used to configure an Agent.
type AgentOption func(*Agent) error

// NominationValueGenerator is a function that generates nomination values for renomination.
type NominationValueGenerator func() uint32

// DefaultNominationValueGenerator returns a generator that starts at 1 and increments for each call.
// This provides a simple, monotonically increasing sequence suitable for renomination.
func DefaultNominationValueGenerator() NominationValueGenerator {
	var counter atomic.Uint32

	return func() uint32 {
		return counter.Add(1)
	}
}

// WithRenomination enables ICE renomination as described in draft-thatcher-ice-renomination-01.
// When enabled, the controlling agent can renominate candidate pairs multiple times
// and the controlled agent follows "last nomination wins" rule.
//
// The generator parameter specifies how nomination values are generated.
// Use DefaultNominationValueGenerator() for a simple incrementing counter,
// or provide a custom generator for more complex scenarios.
//
// Example:
//
//	agent, err := NewAgentWithOptions(config, WithRenomination(DefaultNominationValueGenerator()))
func WithRenomination(generator NominationValueGenerator) AgentOption {
	return func(a *Agent) error {
		if generator == nil {
			return ErrInvalidNominationValueGenerator
		}
		a.enableRenomination = true
		a.nominationValueGenerator = generator

		return nil
	}
}

// WithNominationAttribute sets the STUN attribute type to use for ICE renomination.
// The default value is 0x0030. This can be configured until the attribute is officially
// assigned by IANA for draft-thatcher-ice-renomination.
//
// This option returns an error if the provided attribute type is invalid.
// Currently, validation ensures the attribute is not 0x0000 (reserved).
// Additional validation may be added in the future.
func WithNominationAttribute(attrType uint16) AgentOption {
	return func(a *Agent) error {
		// Basic validation: ensure it's not the reserved 0x0000
		if attrType == 0x0000 {
			return ErrInvalidNominationAttribute
		}

		a.nominationAttribute = stun.AttrType(attrType)

		return nil
	}
}
