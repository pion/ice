// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build go1.25 && !js

package ice

import (
	"context"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/stun/v4"
	"github.com/stretchr/testify/require"
)

func TestUDPMuxWriteWatchdogIdleClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pc := newDeadlineBlockingPacketConn()
		mux := NewUDPMuxDefault(UDPMuxParams{UDPConn: pc})
		defer func() { require.NoError(t, mux.Close()) }()
		idle, err := mux.GetConn("idle", pc.LocalAddr())
		require.NoError(t, err)
		writer, err := mux.GetConn("writer", pc.LocalAddr())
		require.NoError(t, err)

		result := make(chan error, 1)
		go func() {
			_, writeErr := writer.WriteTo([]byte("packet"), pc.LocalAddr())
			result <- writeErr
		}()
		<-pc.writeStarted

		// Candidate shutdown aborts writes before closing its mux connection.
		aborter, ok := idle.(writeAborter)
		require.True(t, ok)
		require.NoError(t, aborter.abortWrite())
		require.NoError(t, idle.Close())
		require.Equal(t, 5*time.Second, mux.writeAbortGrace)
		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case <-pc.writeDeadlineSet:
			require.FailNow(t, "closing idle A expired B's shared write deadline")
		default:
		}
		require.Empty(t, result, "B must remain pending after A's watchdog expires")
		require.NoError(t, mux.Close())
		require.ErrorIs(t, <-result, io.ErrClosedPipe)
	})
}

func TestAgentCloseAbortsBlockedUDPMuxWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, err := NewAgent(WithMulticastDNSMode(MulticastDNSModeDisabled))
		require.NoError(t, err)

		udpConn := newDeadlineBlockingPacketConn()
		udpMux := NewUDPMuxDefault(UDPMuxParams{UDPConn: udpConn})
		defer func() {
			_ = udpMux.Close()
		}()

		muxedConn, err := udpMux.GetConn(agent.localUfrag, udpConn.LocalAddr())
		require.NoError(t, err)

		local, err := NewCandidateHost(&CandidateHostConfig{Network: NetworkTypeUDP4.String(), Address: "192.0.2.1", Port: 1, Component: ComponentRTP})
		require.NoError(t, err)
		local.start(agent, muxedConn, agent.startedCh)

		remote, err := NewCandidateHost(&CandidateHostConfig{Network: NetworkTypeUDP4.String(), Address: "192.0.2.2", Port: 2, Component: ComponentRTP})
		require.NoError(t, err)

		msg, err := stun.Build(stun.BindingRequest, stun.TransactionID)
		require.NoError(t, err)

		runErr := make(chan error, 1)
		go func() {
			runErr <- agent.loop.Run(agent.loop, func(context.Context) {
				agent.sendSTUN(msg, local, remote)
			})
		}()

		select {
		case <-udpConn.writeStarted:
		case <-time.After(6 * time.Second):
			require.Fail(t, "timed out waiting for UDP mux write to block")
		}

		closeErr := make(chan error, 1)
		go func() {
			closeErr <- agent.Close()
		}()

		select {
		case err := <-closeErr:
			require.NoError(t, err)
		case <-time.After(6 * time.Second):
			_ = udpConn.Close()
			<-closeErr
			require.Fail(t, "agent close did not abort blocked UDP mux write")
		}

		require.NoError(t, <-runErr)
	})
}

func TestAgentCloseAbortsBlockedUDPMuxSrflxGatherWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		udpConn := newDeadlineBlockingPacketConn()
		udpMux := NewUniversalUDPMuxDefault(UniversalUDPMuxParams{UDPConn: udpConn})
		defer func() {
			_ = udpMux.Close()
		}()

		agent, err := NewAgent(WithMulticastDNSMode(MulticastDNSModeDisabled), WithNetworkTypes([]NetworkType{NetworkTypeUDP4}), WithCandidateTypes([]CandidateType{CandidateTypeServerReflexive}), WithUDPMuxSrflx(udpMux))
		require.NoError(t, err)
		require.NoError(t, agent.SetURLs([]*stun.URI{{Scheme: stun.SchemeTypeSTUN, Host: "192.0.2.2", Port: 3478}}))

		require.NoError(t, agent.OnCandidate(func(Candidate) {}))
		require.NoError(t, agent.GatherCandidates())

		select {
		case <-udpConn.writeStarted:
		case <-time.After(6 * time.Second):
			require.FailNow(t, "timed out waiting for UDP mux srflx gather write to block")
		}

		closeErr := make(chan error, 1)
		go func() {
			closeErr <- agent.Close()
		}()

		select {
		case err := <-closeErr:
			require.NoError(t, err)
		case <-time.After(6 * time.Second):
			require.FailNow(t, "agent close did not abort blocked UDP mux srflx gather write")
		}
	})
}

func TestAgentCloseDoesNotAbortOtherAgentUDPMuxSrflxGatherWrite(t *testing.T) { //nolint:cyclop
	synctest.Test(t, func(t *testing.T) {
		udpConn := newDeadlineBlockingPacketConn()
		udpMux := NewUniversalUDPMuxDefault(UniversalUDPMuxParams{UDPConn: udpConn})
		defer func() {
			_ = udpMux.Close()
		}()

		newSrflxAgent := func(t *testing.T) *Agent {
			t.Helper()

			agent, err := NewAgent(WithMulticastDNSMode(MulticastDNSModeDisabled), WithNetworkTypes([]NetworkType{NetworkTypeUDP4}), WithCandidateTypes([]CandidateType{CandidateTypeServerReflexive}), WithUDPMuxSrflx(udpMux))
			require.NoError(t, err)
			require.NoError(t, agent.SetURLs([]*stun.URI{{Scheme: stun.SchemeTypeSTUN, Host: "192.0.2.2", Port: 3478}}))

			return agent
		}

		agent1 := newSrflxAgent(t)
		defer func() {
			_ = agent1.Close()
		}()

		agent2 := newSrflxAgent(t)
		defer func() {
			_ = agent2.Close()
		}()

		require.NoError(t, agent2.OnCandidate(func(Candidate) {}))
		require.NoError(t, agent2.GatherCandidates())

		select {
		case <-udpConn.writeStarted:
		case <-time.After(6 * time.Second):
			require.FailNow(t, "timed out waiting for second agent UDP mux srflx gather write to block")
		}

		closeErr := make(chan error, 1)
		go func() {
			closeErr <- agent1.Close()
		}()

		select {
		case err := <-closeErr:
			require.NoError(t, err)
		case <-time.After(6 * time.Second):
			require.FailNow(t, "first agent close blocked")
		}

		select {
		case <-udpConn.writeDeadlineSet:
			require.FailNow(t, "first agent close aborted another agent's UDP mux srflx gather write")
		case <-time.After(5 * time.Second):
		}

		secondCloseErr := make(chan error, 1)
		go func() {
			secondCloseErr <- agent2.Close()
		}()

		select {
		case <-udpConn.writeDeadlineSet:
		case <-time.After(6 * time.Second):
			require.FailNow(t, "second agent close did not abort its own UDP mux srflx gather write")
		}

		select {
		case err := <-secondCloseErr:
			require.NoError(t, err)
		case <-time.After(6 * time.Second):
			require.FailNow(t, "second agent close blocked")
		}
	})
}

func TestAgentCloseClearsSharedUDPMuxAbortDeadlineForOtherAgent(t *testing.T) { //nolint:cyclop
	synctest.Test(t, func(t *testing.T) {
		udpConn := newBlockingDeadlinePacketConn()
		udpMux := NewUDPMuxDefault(UDPMuxParams{UDPConn: udpConn})
		defer func() {
			_ = udpMux.Close()
		}()

		newMuxAgent := func(t *testing.T) *Agent {
			t.Helper()

			agent, err := NewAgent(WithMulticastDNSMode(MulticastDNSModeDisabled), WithNetworkTypes([]NetworkType{NetworkTypeUDP4}), WithCandidateTypes([]CandidateType{CandidateTypeHost}), WithUDPMux(udpMux), WithIncludeLoopback())
			require.NoError(t, err)

			require.NoError(t, agent.gatherCandidatesLocalUDPMux(context.Background(), agent.gatherGeneration, agent.localUfrag))

			return agent
		}

		agent1 := newMuxAgent(t)
		defer func() {
			_ = agent1.Close()
		}()

		agent2 := newMuxAgent(t)
		defer func() {
			_ = agent2.Close()
		}()

		onlyLocalCandidate := func(t *testing.T, agent *Agent) Candidate {
			t.Helper()

			candidates, err := agent.GetLocalCandidates()
			require.NoError(t, err)
			require.Len(t, candidates, 1)

			return candidates[0]
		}

		local1 := onlyLocalCandidate(t, agent1)
		local2 := onlyLocalCandidate(t, agent2)

		remote1, err := NewCandidateHost(&CandidateHostConfig{Network: NetworkTypeUDP4.String(), Address: "192.0.2.2", Port: 2, Component: ComponentRTP})
		require.NoError(t, err)

		remote2, err := NewCandidateHost(&CandidateHostConfig{Network: NetworkTypeUDP4.String(), Address: "192.0.2.3", Port: 3, Component: ComponentRTP})
		require.NoError(t, err)

		msg, err := stun.Build(stun.BindingRequest, stun.TransactionID)
		require.NoError(t, err)

		firstRunErr := make(chan error, 1)
		go func() {
			firstRunErr <- agent1.loop.Run(agent1.loop, func(context.Context) {
				agent1.sendSTUN(msg, local1, remote1)
			})
		}()

		select {
		case <-udpConn.firstWriteStarted:
		case <-time.After(6 * time.Second):
			require.FailNow(t, "timed out waiting for first agent write to block")
		}

		closeErr := make(chan error, 1)
		go func() {
			closeErr <- agent1.Close()
		}()

		select {
		case <-udpConn.deadlineSet:
		case <-time.After(6 * time.Second):
			require.FailNow(t, "timed out waiting for UDP mux abort deadline")
		}

		close(udpConn.allowFirstReturn)

		select {
		case <-udpConn.deadlineCleared:
		case <-time.After(6 * time.Second):
			require.FailNow(t, "timed out waiting for UDP mux abort deadline to clear")
		}

		select {
		case err := <-closeErr:
			require.NoError(t, err)
		case <-time.After(6 * time.Second):
			require.FailNow(t, "timed out waiting for first agent close")
		}

		require.NoError(t, <-firstRunErr)

		select {
		case <-udpConn.closed:
			require.FailNow(t, "shared UDP socket was closed by first agent")
		default:
		}

		const payload = "second"
		secondResult := make(chan struct {
			n   int
			err error
		}, 1)
		go func() {
			var n int
			var writeErr error
			runErr := agent2.loop.Run(agent2.loop, func(context.Context) {
				n, writeErr = local2.writeTo([]byte(payload), remote2)
			})
			if writeErr == nil {
				writeErr = runErr
			}
			secondResult <- struct {
				n   int
				err error
			}{n: n, err: writeErr}
		}()

		select {
		case result := <-secondResult:
			require.NoError(t, result.err)
			require.Equal(t, len(payload), result.n)
		case <-time.After(6 * time.Second):
			require.FailNow(t, "timed out waiting for second agent write")
		}
	})
}
