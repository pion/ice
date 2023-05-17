// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"testing"
	"time"

	"github.com/pion/transport/v2/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCandidatePairStats(t *testing.T) {
	require := require.New(t)
	report := test.CheckRoutines(t)
	defer report()

	// Avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	a, err := NewAgent(&AgentConfig{})
	require.NoError(err, "Failed to create agent")

	hostConfig := &CandidateHostConfig{
		Network:   "udp",
		Address:   "192.168.1.1",
		Port:      19216,
		Component: 1,
	}
	hostLocal, err := NewCandidateHost(hostConfig)
	require.NoError(err, "Failed to construct local host candidate")

	relayConfig := &CandidateRelayConfig{
		Network:   "udp",
		Address:   "1.2.3.4",
		Port:      2340,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43210,
	}
	relayRemote, err := NewCandidateRelay(relayConfig)
	require.NoError(err, "Failed to construct remote relay candidate")

	srflxConfig := &CandidateServerReflexiveConfig{
		Network:   "udp",
		Address:   "10.10.10.2",
		Port:      19218,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43212,
	}
	srflxRemote, err := NewCandidateServerReflexive(srflxConfig)
	require.NoError(err, "Failed to construct remote srflx candidate")

	prflxConfig := &CandidatePeerReflexiveConfig{
		Network:   "udp",
		Address:   "10.10.10.2",
		Port:      19217,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43211,
	}
	prflxRemote, err := NewCandidatePeerReflexive(prflxConfig)
	require.NoError(err, "Failed to construct remote prflx candidate")

	hostConfig = &CandidateHostConfig{
		Network:   "udp",
		Address:   "1.2.3.5",
		Port:      12350,
		Component: 1,
	}
	hostRemote, err := NewCandidateHost(hostConfig)
	require.NoError(err, "Failed to construct remote host candidate")

	for _, remote := range []Candidate{relayRemote, srflxRemote, prflxRemote, hostRemote} {
		p := a.findPair(hostLocal, remote)

		if p == nil {
			a.addPair(hostLocal, remote)
		}
	}

	p := a.findPair(hostLocal, prflxRemote)
	p.state = CandidatePairStateFailed

	stats := a.GetCandidatePairsStats()
	require.Len(stats, 4, "Expected 4 candidate pairs stats")

	var relayPairStat, srflxPairStat, prflxPairStat, hostPairStat CandidatePairStats

	for _, cps := range stats {
		require.Equal(cps.LocalCandidateID, hostLocal.ID(), "Invalid local candidate id")

		switch cps.RemoteCandidateID {
		case relayRemote.ID():
			relayPairStat = cps
		case srflxRemote.ID():
			srflxPairStat = cps
		case prflxRemote.ID():
			prflxPairStat = cps
		case hostRemote.ID():
			hostPairStat = cps
		default:
			require.Fail("Invalid remote candidate ID")
		}
	}

	require.Equal(relayPairStat.RemoteCandidateID, relayRemote.ID(), "Missing host-relay pair stat")
	require.Equal(srflxPairStat.RemoteCandidateID, srflxRemote.ID(), "Missing host-srflx pair stat")
	require.Equal(prflxPairStat.RemoteCandidateID, prflxRemote.ID(), "Missing host-prflx pair stat")
	require.Equal(hostPairStat.RemoteCandidateID, hostRemote.ID(), "Missing host-host pair stat")
	require.Equalf(prflxPairStat.State, CandidatePairStateFailed, "Expected host-prflx pair to have state failed, it has state %s instead", prflxPairStat.State)

	assert.NoError(t, a.Close())
}

func TestLocalCandidateStats(t *testing.T) {
	require := require.New(t)
	report := test.CheckRoutines(t)
	defer report()

	// Avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	a, err := NewAgent(&AgentConfig{})
	require.NoError(err, "Failed to create agent")

	hostConfig := &CandidateHostConfig{
		Network:   "udp",
		Address:   "192.168.1.1",
		Port:      19216,
		Component: 1,
	}
	hostLocal, err := NewCandidateHost(hostConfig)
	require.NoError(err, "Failed to construct local host candidate")

	srflxConfig := &CandidateServerReflexiveConfig{
		Network:   "udp",
		Address:   "192.168.1.1",
		Port:      19217,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43212,
	}
	srflxLocal, err := NewCandidateServerReflexive(srflxConfig)
	require.NoError(err, "Failed to construct local srflx candidate")

	a.localCandidates[NetworkTypeUDP4] = []Candidate{hostLocal, srflxLocal}

	localStats := a.GetLocalCandidatesStats()
	require.Len(localStats, 2, "Expected 2 local candidates stats")

	var hostLocalStat, srflxLocalStat CandidateStats
	for _, stats := range localStats {
		var candidate Candidate
		switch stats.ID {
		case hostLocal.ID():
			hostLocalStat = stats
			candidate = hostLocal
		case srflxLocal.ID():
			srflxLocalStat = stats
			candidate = srflxLocal
		default:
			require.Fail("Invalid local candidate ID")
		}

		require.Equal(stats.CandidateType, candidate.Type(), "Invalid stats CandidateType")
		require.Equal(stats.Priority, candidate.Priority(), "Invalid stats CandidateType")
		require.Equal(stats.IP, candidate.Address(), "Invalid stats IP")
	}

	require.Equal(hostLocalStat.ID, hostLocal.ID(), "Missing host local stat")
	require.Equal(srflxLocalStat.ID, srflxLocal.ID(), "Missing srflx local stat")

	assert.NoError(t, a.Close())
}

func TestRemoteCandidateStats(t *testing.T) {
	require := require.New(t)
	report := test.CheckRoutines(t)
	defer report()

	// Avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	a, err := NewAgent(&AgentConfig{})
	require.NoError(err, "Failed to create agent")

	relayConfig := &CandidateRelayConfig{
		Network:   "udp",
		Address:   "1.2.3.4",
		Port:      12340,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43210,
	}
	relayRemote, err := NewCandidateRelay(relayConfig)
	require.NoError(err, "Failed to construct remote relay candidate")

	srflxConfig := &CandidateServerReflexiveConfig{
		Network:   "udp",
		Address:   "10.10.10.2",
		Port:      19218,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43212,
	}
	srflxRemote, err := NewCandidateServerReflexive(srflxConfig)
	require.NoError(err, "Failed to construct remote srflx candidate")

	prflxConfig := &CandidatePeerReflexiveConfig{
		Network:   "udp",
		Address:   "10.10.10.2",
		Port:      19217,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43211,
	}
	prflxRemote, err := NewCandidatePeerReflexive(prflxConfig)
	require.NoError(err, "Failed to construct remote prflx candidate")

	hostConfig := &CandidateHostConfig{
		Network:   "udp",
		Address:   "1.2.3.5",
		Port:      12350,
		Component: 1,
	}
	hostRemote, err := NewCandidateHost(hostConfig)
	require.NoError(err, "Failed to construct remote host candidate")

	a.remoteCandidates[NetworkTypeUDP4] = []Candidate{relayRemote, srflxRemote, prflxRemote, hostRemote}

	remoteStats := a.GetRemoteCandidatesStats()
	require.Len(remoteStats, 4, "Expected 4 remote candidates stats")

	var relayRemoteStat, srflxRemoteStat, prflxRemoteStat, hostRemoteStat CandidateStats
	for _, stats := range remoteStats {
		var candidate Candidate
		switch stats.ID {
		case relayRemote.ID():
			relayRemoteStat = stats
			candidate = relayRemote
		case srflxRemote.ID():
			srflxRemoteStat = stats
			candidate = srflxRemote
		case prflxRemote.ID():
			prflxRemoteStat = stats
			candidate = prflxRemote
		case hostRemote.ID():
			hostRemoteStat = stats
			candidate = hostRemote
		default:
			require.Fail("Invalid remote candidate ID")
		}

		require.Equal(stats.CandidateType, candidate.Type(), "Invalid stats CandidateType")
		require.Equal(stats.Priority, candidate.Priority(), "Invalid stats CandidateType")
		require.Equal(stats.IP, candidate.Address(), "Invalid stats IP")
	}

	require.Equal(relayRemoteStat.ID, relayRemote.ID(), "Missing relay remote stat")
	require.Equal(srflxRemoteStat.ID, srflxRemote.ID(), "Missing srflx remote stat")
	require.Equal(prflxRemoteStat.ID, prflxRemote.ID(), "Missing prflx remote stat")
	require.Equal(hostRemoteStat.ID, hostRemote.ID(), "Missing host remote stat")

	assert.NoError(t, a.Close())
}
