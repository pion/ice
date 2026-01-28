// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"net"
	"testing"
	"time"

	"github.com/pion/ice/v4/internal/fakenet"
	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/assert"
)

const (
	testLocalUfrag  = "localufrag"
	testLocalPwd    = "localpwd"
	testRemoteUfrag = "remoteufrag"
	testRemotePwd   = "remotepwd"
)

// Mock packet conn that captures sent packets.
type mockPacketConnWithCapture struct {
	sentPackets [][]byte
	sentAddrs   []net.Addr
}

func (m *mockPacketConnWithCapture) ReadFrom([]byte) (n int, addr net.Addr, err error) {
	return 0, nil, nil
}

func (m *mockPacketConnWithCapture) WriteTo(b []byte, addr net.Addr) (n int, err error) {
	// Capture the packet
	packet := make([]byte, len(b))
	copy(packet, b)
	m.sentPackets = append(m.sentPackets, packet)
	m.sentAddrs = append(m.sentAddrs, addr)

	return len(b), nil
}

func (m *mockPacketConnWithCapture) Close() error                     { return nil }
func (m *mockPacketConnWithCapture) LocalAddr() net.Addr              { return nil }
func (m *mockPacketConnWithCapture) SetDeadline(time.Time) error      { return nil }
func (m *mockPacketConnWithCapture) SetReadDeadline(time.Time) error  { return nil }
func (m *mockPacketConnWithCapture) SetWriteDeadline(time.Time) error { return nil }

// createRenominationTestAgent creates a test agent with renomination enabled and returns local/remote candidates.
func createRenominationTestAgent(t *testing.T, controlling bool) (*Agent, Candidate, Candidate) {
	t.Helper()

	agent, err := NewAgentWithOptions(WithRenomination(func() uint32 { return 1 }))
	assert.NoError(t, err)

	agent.isControlling.Store(controlling)

	local, err := NewCandidateHost(&CandidateHostConfig{
		Network:   "udp",
		Address:   "127.0.0.1",
		Port:      12345,
		Component: 1,
	})
	assert.NoError(t, err)

	remote, err := NewCandidateHost(&CandidateHostConfig{
		Network:   "udp",
		Address:   "127.0.0.1",
		Port:      54321,
		Component: 1,
	})
	assert.NoError(t, err)

	return agent, local, remote
}

func TestNominationAttribute(t *testing.T) {
	t.Run("AddTo and GetFrom", func(t *testing.T) {
		m := &stun.Message{}
		attr := NominationAttribute{Value: 0x123456}

		err := attr.AddTo(m)
		assert.NoError(t, err)

		var parsed NominationAttribute
		err = parsed.GetFrom(m)
		assert.NoError(t, err)
		assert.Equal(t, uint32(0x123456), parsed.Value)
	})

	t.Run("24-bit value boundary", func(t *testing.T) {
		m := &stun.Message{}
		maxValue := uint32((1 << 24) - 1) // 24-bit max value
		attr := NominationAttribute{Value: maxValue}

		err := attr.AddTo(m)
		assert.NoError(t, err)

		var parsed NominationAttribute
		err = parsed.GetFrom(m)
		assert.NoError(t, err)
		assert.Equal(t, maxValue, parsed.Value)
	})

	t.Run("String representation", func(t *testing.T) {
		attr := NominationAttribute{Value: 12345}
		str := attr.String()
		assert.Contains(t, str, "NOMINATION")
		assert.Contains(t, str, "12345")
	})

	t.Run("Nomination helper function", func(t *testing.T) {
		attr := Nomination(42)
		assert.Equal(t, uint32(42), attr.Value)
	})
}

func TestRenominationConfiguration(t *testing.T) {
	nominationCounter := uint32(0)

	agent, err := NewAgentWithOptions(WithRenomination(func() uint32 {
		nominationCounter++

		return nominationCounter
	}))
	assert.NoError(t, err)
	defer func() {
		assert.NoError(t, agent.Close())
	}()

	assert.True(t, agent.enableRenomination)
	assert.NotNil(t, agent.nominationValueGenerator)

	// Test nomination value generation
	value1 := agent.nominationValueGenerator()
	value2 := agent.nominationValueGenerator()
	assert.Equal(t, uint32(1), value1)
	assert.Equal(t, uint32(2), value2)
}

func TestControlledSelectorNominationAcceptance(t *testing.T) {
	agent, err := NewAgentWithOptions(WithRenomination(DefaultNominationValueGenerator()))
	assert.NoError(t, err)
	defer func() {
		assert.NoError(t, agent.Close())
	}()

	selector := &controlledSelector{
		agent: agent,
		log:   agent.log,
	}
	selector.Start()

	// First nomination should be accepted
	nomination1 := uint32(5)
	assert.True(t, selector.shouldAcceptNomination(&nomination1))

	// Higher nomination should be accepted
	nomination2 := uint32(10)
	assert.True(t, selector.shouldAcceptNomination(&nomination2))

	// Lower nomination should be rejected
	nomination3 := uint32(7)
	assert.False(t, selector.shouldAcceptNomination(&nomination3))

	// Equal nomination should be rejected
	nomination4 := uint32(10)
	assert.False(t, selector.shouldAcceptNomination(&nomination4))

	// Nil nomination should be accepted (standard ICE)
	assert.True(t, selector.shouldAcceptNomination(nil))
}

func TestControlledSelectorNominationDisabled(t *testing.T) {
	config := &AgentConfig{
		// Renomination disabled by default
	}

	agent, err := NewAgent(config)
	assert.NoError(t, err)
	defer func() {
		assert.NoError(t, agent.Close())
	}()

	selector := &controlledSelector{
		agent: agent,
		log:   agent.log,
	}
	selector.Start()

	// Standard ICE nomination (no value) should be accepted
	assert.True(t, selector.shouldAcceptNomination(nil))

	// When controlling side uses renomination (sends nomination values),
	// controlled side should apply "last nomination wins" regardless of local config
	nomination1 := uint32(5)
	assert.True(t, selector.shouldAcceptNomination(&nomination1))

	nomination2 := uint32(3) // Lower value should be rejected
	assert.False(t, selector.shouldAcceptNomination(&nomination2))

	nomination3 := uint32(8) // Higher value should be accepted
	assert.True(t, selector.shouldAcceptNomination(&nomination3))
}

func TestAgentRenominateCandidate(t *testing.T) {
	t.Run("controlling agent can renominate", func(t *testing.T) {
		nominationCounter := uint32(0)
		agent, err := NewAgentWithOptions(WithRenomination(func() uint32 {
			nominationCounter++

			return nominationCounter
		}))
		assert.NoError(t, err)
		defer func() {
			assert.NoError(t, agent.Close())
		}()

		// Set up credentials for STUN authentication
		agent.localUfrag = testLocalUfrag
		agent.localPwd = testLocalPwd
		agent.remoteUfrag = testRemoteUfrag
		agent.remotePwd = testRemotePwd

		// Set agent as controlling
		agent.isControlling.Store(true)

		// Create test candidates with mock connection
		local, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      12345,
			Component: 1,
		})
		assert.NoError(t, err)

		remote, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      54321,
			Component: 1,
		})
		assert.NoError(t, err)

		// Mock the connection for the local candidate to avoid nil pointer
		mockConn := &fakenet.MockPacketConn{}
		local.conn = mockConn

		// Add pair to agent
		pair := agent.addPair(local, remote)
		pair.state = CandidatePairStateSucceeded

		// Test renomination
		err = agent.RenominateCandidate(local, remote)
		assert.NoError(t, err)
	})

	t.Run("non-controlling agent cannot renominate", func(t *testing.T) {
		agent, local, remote := createRenominationTestAgent(t, false)
		defer func() {
			assert.NoError(t, agent.Close())
		}()

		err := agent.RenominateCandidate(local, remote)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "only controlling agent can renominate")
	})

	t.Run("renomination when disabled", func(t *testing.T) {
		config := &AgentConfig{
			// Renomination disabled by default
		}

		agent, err := NewAgent(config)
		assert.NoError(t, err)
		defer func() {
			assert.NoError(t, agent.Close())
		}()

		agent.isControlling.Store(true)

		local, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      12345,
			Component: 1,
		})
		assert.NoError(t, err)

		remote, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      54321,
			Component: 1,
		})
		assert.NoError(t, err)

		err = agent.RenominateCandidate(local, remote)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "renomination is not enabled")
	})

	t.Run("renomination with non-existent candidate pair", func(t *testing.T) {
		agent, local, remote := createRenominationTestAgent(t, true)
		defer func() {
			assert.NoError(t, agent.Close())
		}()

		// Don't add pair to agent - should fail
		err := agent.RenominateCandidate(local, remote)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "candidate pair not found")
	})
}

func TestSendNominationRequest(t *testing.T) {
	t.Run("STUN message contains nomination attribute", func(t *testing.T) {
		nominationCounter := uint32(0)
		agent, err := NewAgentWithOptions(WithRenomination(func() uint32 {
			nominationCounter++

			return nominationCounter
		}))
		assert.NoError(t, err)
		defer func() {
			assert.NoError(t, agent.Close())
		}()

		// Set up credentials for STUN authentication
		agent.localUfrag = testLocalUfrag
		agent.localPwd = testLocalPwd
		agent.remoteUfrag = testRemoteUfrag
		agent.remotePwd = testRemotePwd

		agent.isControlling.Store(true)

		// Create test candidates
		local, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      12345,
			Component: 1,
		})
		assert.NoError(t, err)

		remote, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      54321,
			Component: 1,
		})
		assert.NoError(t, err)

		// Mock connection to capture sent messages
		mockConn := &mockPacketConnWithCapture{}
		local.conn = mockConn

		pair := agent.addPair(local, remote)
		pair.state = CandidatePairStateSucceeded

		// Test sendNominationRequest directly
		nominationValue := uint32(123)
		err = agent.sendNominationRequest(pair, nominationValue)
		assert.NoError(t, err)

		// Verify message was sent
		assert.True(t, len(mockConn.sentPackets) > 0)

		// Parse the sent STUN message
		msg := &stun.Message{}
		err = msg.UnmarshalBinary(mockConn.sentPackets[0])
		assert.NoError(t, err)

		// Verify it's a binding request
		assert.True(t, msg.Type.Method == stun.MethodBinding)
		assert.True(t, msg.Type.Class == stun.ClassRequest)

		// Verify USE-CANDIDATE is present
		assert.True(t, msg.Contains(stun.AttrUseCandidate))

		// Verify nomination attribute is present
		var nomination NominationAttribute
		err = nomination.GetFrom(msg)
		assert.NoError(t, err)
		assert.Equal(t, nominationValue, nomination.Value)
	})

	t.Run("STUN message without nomination when disabled", func(t *testing.T) {
		config := &AgentConfig{
			// Renomination disabled by default
		}

		agent, err := NewAgent(config)
		assert.NoError(t, err)
		defer func() {
			assert.NoError(t, agent.Close())
		}()

		// Set up credentials
		agent.localUfrag = testLocalUfrag
		agent.localPwd = testLocalPwd
		agent.remoteUfrag = testRemoteUfrag
		agent.remotePwd = testRemotePwd

		agent.isControlling.Store(true)

		local, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      12345,
			Component: 1,
		})
		assert.NoError(t, err)

		remote, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      54321,
			Component: 1,
		})
		assert.NoError(t, err)

		mockConn := &mockPacketConnWithCapture{}
		local.conn = mockConn

		pair := agent.addPair(local, remote)

		// Send nomination with value 0 (should not include nomination attribute)
		err = agent.sendNominationRequest(pair, 0)
		assert.NoError(t, err)

		// Parse the sent message
		msg := &stun.Message{}
		err = msg.UnmarshalBinary(mockConn.sentPackets[0])
		assert.NoError(t, err)

		// Verify USE-CANDIDATE is present
		assert.True(t, msg.Contains(stun.AttrUseCandidate))

		// Verify nomination attribute is NOT present
		var nomination NominationAttribute
		err = nomination.GetFrom(msg)
		assert.Error(t, err) // Should fail since attribute is not present
	})
}

func TestRenominationErrorCases(t *testing.T) {
	t.Run("getNominationValue with nil generator", func(t *testing.T) {
		// Try to create agent with nil generator - should fail
		_, err := NewAgentWithOptions(WithRenomination(nil))
		assert.ErrorIs(t, err, ErrInvalidNominationValueGenerator)

		// Create agent without renomination for testing
		agent, err := NewAgentWithOptions()
		assert.NoError(t, err)
		defer func() {
			assert.NoError(t, agent.Close())
		}()

		// Should return 0 when no generator is set
		value := agent.getNominationValue()
		assert.Equal(t, uint32(0), value)
	})

	t.Run("STUN message build with invalid attributes", func(t *testing.T) {
		agent, err := NewAgentWithOptions(WithRenomination(func() uint32 { return 1 }))
		assert.NoError(t, err)
		defer func() {
			assert.NoError(t, agent.Close())
		}()

		// Set up minimal credentials but missing remote password
		agent.localUfrag = "localufrag"
		agent.localPwd = "localpwd"
		agent.remoteUfrag = "remoteufrag"
		// agent.remotePwd = "" // Missing remote password

		agent.isControlling.Store(true)

		local, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      12345,
			Component: 1,
		})
		assert.NoError(t, err)

		remote, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      54321,
			Component: 1,
		})
		assert.NoError(t, err)

		mockConn := &mockPacketConnWithCapture{}
		local.conn = mockConn

		pair := agent.addPair(local, remote)

		// This should succeed even with missing remote password
		// as the STUN library will still build the message
		err = agent.sendNominationRequest(pair, 1)
		assert.NoError(t, err)
	})
}

func TestNominationValueBoundaries(t *testing.T) {
	t.Run("24-bit maximum value", func(t *testing.T) {
		maxValue := uint32((1 << 24) - 1) // 0xFFFFFF
		attr := NominationAttribute{Value: maxValue}

		m := &stun.Message{}
		err := attr.AddTo(m)
		assert.NoError(t, err)

		var parsed NominationAttribute
		err = parsed.GetFrom(m)
		assert.NoError(t, err)
		assert.Equal(t, maxValue, parsed.Value)
	})

	t.Run("zero nomination value", func(t *testing.T) {
		attr := NominationAttribute{Value: 0}

		m := &stun.Message{}
		err := attr.AddTo(m)
		assert.NoError(t, err)

		var parsed NominationAttribute
		err = parsed.GetFrom(m)
		assert.NoError(t, err)
		assert.Equal(t, uint32(0), parsed.Value)
	})

	t.Run("nomination value overflow", func(t *testing.T) {
		// Test value larger than 24-bit
		overflowValue := uint32(1 << 25) // Larger than 24-bit max
		attr := NominationAttribute{Value: overflowValue}

		m := &stun.Message{}
		err := attr.AddTo(m)
		assert.NoError(t, err)

		var parsed NominationAttribute
		err = parsed.GetFrom(m)
		assert.NoError(t, err)

		// Should be truncated to 24-bit value
		expectedValue := overflowValue & 0xFFFFFF
		assert.Equal(t, expectedValue, parsed.Value)
	})

	t.Run("invalid attribute size", func(t *testing.T) {
		m := &stun.Message{}
		// Add a nomination attribute with invalid size (too short)
		m.Add(DefaultNominationAttribute, []byte{0x01, 0x02}) // Only 2 bytes instead of 4

		var nomination NominationAttribute
		err := nomination.GetFrom(m)
		assert.Error(t, err)
		assert.Equal(t, stun.ErrAttributeSizeInvalid, err)
	})

	t.Run("configurable nomination attribute type", func(t *testing.T) {
		// Test with custom attribute type
		customAttrType := stun.AttrType(0x0040)
		attr := NominationAttribute{Value: 12345}

		m := &stun.Message{}
		err := attr.AddToWithType(m, customAttrType)
		assert.NoError(t, err)

		// Try to read with default type - should fail
		var parsed1 NominationAttribute
		err = parsed1.GetFrom(m)
		assert.Error(t, err)

		// Read with custom type - should succeed
		var parsed2 NominationAttribute
		err = parsed2.GetFromWithType(m, customAttrType)
		assert.NoError(t, err)
		assert.Equal(t, uint32(12345), parsed2.Value)
	})

	t.Run("NominationSetter with custom attribute type", func(t *testing.T) {
		customAttrType := stun.AttrType(0x0050)
		setter := NominationSetter{
			Value:    98765,
			AttrType: customAttrType,
		}

		m := &stun.Message{}
		err := setter.AddTo(m)
		assert.NoError(t, err)

		// Verify the attribute was added with custom type
		var parsed NominationAttribute
		err = parsed.GetFromWithType(m, customAttrType)
		assert.NoError(t, err)
		assert.Equal(t, uint32(98765), parsed.Value)

		// Verify it wasn't added with default type
		var parsedDefault NominationAttribute
		err = parsedDefault.GetFrom(m)
		assert.Error(t, err)
	})
}

func TestControlledSelectorWithActualSTUNMessages(t *testing.T) {
	t.Run("HandleBindingRequest with nomination attribute", func(t *testing.T) {
		agent, err := NewAgentWithOptions(WithRenomination(DefaultNominationValueGenerator()))
		assert.NoError(t, err)
		defer func() {
			assert.NoError(t, agent.Close())
		}()

		// Set up credentials for STUN
		agent.localUfrag = testLocalUfrag
		agent.localPwd = testLocalPwd
		agent.remoteUfrag = testRemoteUfrag
		agent.remotePwd = testRemotePwd

		selector := &controlledSelector{
			agent: agent,
			log:   agent.log,
		}
		selector.Start()

		// Create test candidates
		local, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      12345,
			Component: 1,
		})
		assert.NoError(t, err)

		remote, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      54321,
			Component: 1,
		})
		assert.NoError(t, err)

		// Mock connection for response
		mockConn := &mockPacketConnWithCapture{}
		local.conn = mockConn

		// Create STUN binding request with nomination and USE-CANDIDATE
		msg, err := stun.Build(
			stun.BindingRequest,
			stun.TransactionID,
			stun.NewUsername(agent.localUfrag+":"+agent.remoteUfrag),
			UseCandidate(),
			Nomination(5), // First nomination value
			stun.NewShortTermIntegrity(agent.localPwd),
			stun.Fingerprint,
		)
		assert.NoError(t, err)

		// Handle the binding request
		selector.HandleBindingRequest(msg, local, remote)

		// Verify selector accepted the nomination
		assert.NotNil(t, selector.lastNomination)
		assert.Equal(t, uint32(5), *selector.lastNomination)

		// Create another STUN request with higher nomination value
		msg2, err := stun.Build(
			stun.BindingRequest,
			stun.TransactionID,
			stun.NewUsername(agent.localUfrag+":"+agent.remoteUfrag),
			UseCandidate(),
			Nomination(10), // Higher nomination value
			stun.NewShortTermIntegrity(agent.localPwd),
			stun.Fingerprint,
		)
		assert.NoError(t, err)

		// Handle the second binding request
		selector.HandleBindingRequest(msg2, local, remote)

		// Should accept higher nomination
		assert.Equal(t, uint32(10), *selector.lastNomination)

		// Create another STUN request with lower nomination value
		msg3, err := stun.Build(
			stun.BindingRequest,
			stun.TransactionID,
			stun.NewUsername(agent.localUfrag+":"+agent.remoteUfrag),
			UseCandidate(),
			Nomination(7), // Lower nomination value
			stun.NewShortTermIntegrity(agent.localPwd),
			stun.Fingerprint,
		)
		assert.NoError(t, err)

		// Handle the third binding request
		selector.HandleBindingRequest(msg3, local, remote)

		// Should reject lower nomination (lastNomination should remain 10)
		assert.Equal(t, uint32(10), *selector.lastNomination)
	})

	t.Run("HandleBindingRequest without nomination attribute", func(t *testing.T) {
		agent, err := NewAgentWithOptions(WithRenomination(DefaultNominationValueGenerator()))
		assert.NoError(t, err)
		defer func() {
			assert.NoError(t, agent.Close())
		}()

		agent.localUfrag = testLocalUfrag
		agent.localPwd = testLocalPwd
		agent.remoteUfrag = testRemoteUfrag
		agent.remotePwd = testRemotePwd

		selector := &controlledSelector{
			agent: agent,
			log:   agent.log,
		}
		selector.Start()

		local, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      12345,
			Component: 1,
		})
		assert.NoError(t, err)

		remote, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      54321,
			Component: 1,
		})
		assert.NoError(t, err)

		mockConn := &mockPacketConnWithCapture{}
		local.conn = mockConn

		// Create STUN binding request without nomination (standard ICE)
		msg, err := stun.Build(
			stun.BindingRequest,
			stun.TransactionID,
			stun.NewUsername(agent.localUfrag+":"+agent.remoteUfrag),
			UseCandidate(),
			// No nomination attribute
			stun.NewShortTermIntegrity(agent.localPwd),
			stun.Fingerprint,
		)
		assert.NoError(t, err)

		// Handle the binding request
		selector.HandleBindingRequest(msg, local, remote)

		// Without nomination attribute, lastNomination should remain nil
		assert.Nil(t, selector.lastNomination)
	})
}

func TestInvalidRenominationConfig(t *testing.T) {
	t.Run("nil nomination generator with renomination enabled", func(t *testing.T) {
		config := &AgentConfig{}

		// Without renomination, agent should work fine
		agent, err := NewAgent(config)
		assert.NoError(t, err)
		defer func() {
			assert.NoError(t, agent.Close())
		}()

		// Agent should be created successfully without renomination
		assert.False(t, agent.enableRenomination)
		assert.Nil(t, agent.nominationValueGenerator)

		// getNominationValue should return 0
		value := agent.getNominationValue()
		assert.Equal(t, uint32(0), value)
	})

	t.Run("different generator behaviors", func(t *testing.T) {
		// Test constant generator
		agent1, err := NewAgentWithOptions(WithRenomination(func() uint32 { return 42 }))
		assert.NoError(t, err)
		defer func() {
			assert.NoError(t, agent1.Close())
		}()

		value1 := agent1.getNominationValue()
		value2 := agent1.getNominationValue()
		assert.Equal(t, uint32(42), value1)
		assert.Equal(t, uint32(42), value2)

		// Test incrementing generator
		counter := uint32(0)
		agent2, err := NewAgentWithOptions(WithRenomination(func() uint32 {
			counter++

			return counter
		}))
		assert.NoError(t, err)
		defer func() {
			assert.NoError(t, agent2.Close())
		}()

		value3 := agent2.getNominationValue()
		value4 := agent2.getNominationValue()
		assert.Equal(t, uint32(1), value3)
		assert.Equal(t, uint32(2), value4)
	})

	t.Run("controlled agent handles renomination regardless of local config", func(t *testing.T) {
		// Create controlled agent with renomination DISABLED
		controlledAgent, err := NewAgent(&AgentConfig{
			// Renomination disabled by default // Disabled locally
		})
		assert.NoError(t, err)
		defer func() {
			assert.NoError(t, controlledAgent.Close())
		}()

		// Set up as controlled (non-controlling)
		controlledAgent.isControlling.Store(false)

		// Create controlled selector to test nomination handling
		selector := &controlledSelector{
			agent: controlledAgent,
			log:   controlledAgent.log,
		}

		// Test 1: Should accept nomination without value (standard ICE)
		assert.True(t, selector.shouldAcceptNomination(nil))

		// Test 2: Should accept first nomination with value (renomination from controlling side)
		value1 := uint32(1)
		assert.True(t, selector.shouldAcceptNomination(&value1))
		assert.Equal(t, &value1, selector.lastNomination)

		// Test 3: Should accept higher nomination value
		value2 := uint32(2)
		assert.True(t, selector.shouldAcceptNomination(&value2))
		assert.Equal(t, &value2, selector.lastNomination)

		// Test 4: Should reject lower nomination value
		value0 := uint32(0)
		assert.False(t, selector.shouldAcceptNomination(&value0))
		assert.Equal(t, &value2, selector.lastNomination) // Should remain unchanged
	})
}

func TestAgentWithCustomNominationAttribute(t *testing.T) {
	t.Run("agent uses custom nomination attribute with option", func(t *testing.T) {
		customAttr := uint16(0x0042)

		// Create agent with custom nomination attribute using option
		agent, err := NewAgentWithOptions(
			WithRenomination(func() uint32 { return 100 }),
			WithNominationAttribute(customAttr),
		)
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		// Verify the agent has the custom attribute configured
		assert.Equal(t, stun.AttrType(customAttr), agent.nominationAttribute)
	})

	t.Run("agent uses default nomination attribute when not configured", func(t *testing.T) {
		// Create agent without custom nomination attribute
		agentConfig := &AgentConfig{
			NetworkTypes: []NetworkType{NetworkTypeUDP4},
		}

		agent, err := NewAgent(agentConfig)
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		// Verify the agent has the default attribute
		assert.Equal(t, stun.AttrType(0x0030), agent.nominationAttribute)
	})

	t.Run("multiple options can be applied", func(t *testing.T) {
		customAttr := uint16(0x0055)

		// Test that multiple options can be applied
		agent, err := NewAgentWithOptions(
			WithRenomination(func() uint32 { return 200 }),
			WithNominationAttribute(customAttr),
		)
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.Equal(t, stun.AttrType(customAttr), agent.nominationAttribute)
	})

	t.Run("WithNominationAttribute returns error for invalid value", func(t *testing.T) {
		// Test that 0x0000 is rejected as invalid
		_, err := NewAgentWithOptions(WithNominationAttribute(0x0000))
		assert.ErrorIs(t, err, ErrInvalidNominationAttribute)
	})
}
