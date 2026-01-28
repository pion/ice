// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAgentConfig_initWithDefaults(t *testing.T) {
	relayAcceptanceMinWait := 5 * time.Second
	tests := []struct {
		name   string
		config *AgentConfig
		fn     func(*testing.T, *Agent)
	}{
		{
			"default config",
			&AgentConfig{},
			func(t *testing.T, result *Agent) {
				t.Helper()
				assert.Equal(t, result.relayAcceptanceMinWait, defaultRelayAcceptanceMinWait)
			},
		},
		{
			"multiple relay candidate types",
			&AgentConfig{CandidateTypes: []CandidateType{CandidateTypeHost, CandidateTypeServerReflexive}},
			func(t *testing.T, result *Agent) {
				t.Helper()
				assert.Equal(t, result.relayAcceptanceMinWait, defaultRelayAcceptanceMinWait)
			},
		},
		{
			"host only candidate type",
			&AgentConfig{CandidateTypes: []CandidateType{CandidateTypeHost}},
			func(t *testing.T, result *Agent) {
				t.Helper()
				assert.Equal(t, result.relayAcceptanceMinWait, defaultRelayAcceptanceMinWait)
			},
		},
		{
			"relay only candidate type",
			&AgentConfig{CandidateTypes: []CandidateType{CandidateTypeRelay}},
			func(t *testing.T, result *Agent) {
				t.Helper()
				assert.Equal(t, result.relayAcceptanceMinWait, defaultRelayOnlyAcceptanceMinWait)
			},
		},
		{
			"relay only with relayAcceptanceMinWait set",
			&AgentConfig{CandidateTypes: []CandidateType{CandidateTypeRelay}, RelayAcceptanceMinWait: &relayAcceptanceMinWait},
			func(t *testing.T, result *Agent) {
				t.Helper()
				assert.Equal(t, result.relayAcceptanceMinWait, relayAcceptanceMinWait)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent, err := NewAgent(test.config)
			if !assert.NoError(t, err) {
				return
			}
			defer func() { _ = agent.Close() }()
			test.fn(t, agent)
		})
	}
}

func TestDefaultRelayAcceptanceMinWaitForCandidates(t *testing.T) {
	tests := []struct {
		name          string
		candidateType []CandidateType
		expectedWait  time.Duration
	}{
		{
			name:          "relay only",
			candidateType: []CandidateType{CandidateTypeRelay},
			expectedWait:  defaultRelayOnlyAcceptanceMinWait,
		},
		{
			name:          "mixed types",
			candidateType: []CandidateType{CandidateTypeHost, CandidateTypeRelay},
			expectedWait:  defaultRelayAcceptanceMinWait,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expectedWait, defaultRelayAcceptanceMinWaitFor(tc.candidateType))
		})
	}
}
