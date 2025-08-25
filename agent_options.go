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

// WithIncludeLoopback includes loopback addresses in the candidate list.
// By default, loopback addresses are excluded.
//
// Example:
//
//	agent, err := NewAgentWithOptions(WithIncludeLoopback())
func WithIncludeLoopback() AgentOption {
	return func(a *Agent) error {
		a.includeLoopback = true

		return nil
	}
}

// WithTCPPriorityOffset sets a number which is subtracted from the default (UDP) candidate type preference
// for host, srflx and prfx candidate types. It helps to configure relative preference of UDP candidates
// against TCP ones. Relay candidates for TCP and UDP are always 0 and not affected by this setting.
// When not set, defaultTCPPriorityOffset (27) is used.
//
// Example:
//
//	agent, err := NewAgentWithOptions(WithTCPPriorityOffset(50))
func WithTCPPriorityOffset(offset uint16) AgentOption {
	return func(a *Agent) error {
		a.tcpPriorityOffset = offset

		return nil
	}
}

// WithDisableActiveTCP disables Active TCP candidates.
// When TCP is enabled, Active TCP candidates will be created when a new passive TCP remote candidate is added
// unless this option is used.
//
// Example:
//
//	agent, err := NewAgentWithOptions(WithDisableActiveTCP())
func WithDisableActiveTCP() AgentOption {
	return func(a *Agent) error {
		a.disableActiveTCP = true

		return nil
	}
}

// WithBindingRequestHandler sets a handler to allow applications to perform logic on incoming STUN Binding Requests.
// This was implemented to allow users to:
//   - Log incoming Binding Requests for debugging
//   - Implement draft-thatcher-ice-renomination
//   - Implement custom CandidatePair switching logic
//
// Example:
//
//	handler := func(m *stun.Message, local, remote Candidate, pair *CandidatePair) bool {
//		log.Printf("Binding request from %s to %s", remote.Address(), local.Address())
//		return true // Accept the request
//	}
//	agent, err := NewAgentWithOptions(WithBindingRequestHandler(handler))
func WithBindingRequestHandler(
	handler func(m *stun.Message, local, remote Candidate, pair *CandidatePair) bool,
) AgentOption {
	return func(a *Agent) error {
		a.userBindingRequestHandler = handler

		return nil
	}
}

// WithEnableUseCandidateCheckPriority enables checking for equal or higher priority when
// switching selected candidate pair if the peer requests USE-CANDIDATE and agent is a lite agent.
// This is disabled by default, i.e. when peer requests USE-CANDIDATE, the selected pair will be
// switched to that irrespective of relative priority between current selected pair
// and priority of the pair being switched to.
//
// Example:
//
//	agent, err := NewAgentWithOptions(WithEnableUseCandidateCheckPriority())
func WithEnableUseCandidateCheckPriority() AgentOption {
	return func(a *Agent) error {
		a.enableUseCandidateCheckPriority = true

		return nil
	}
}
