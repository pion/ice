// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/stretchr/testify/require"
)

const localhostIPStr = "127.0.0.1"

func TestCandidateTypePreference(t *testing.T) {
	req := require.New(t)

	hostDefaultPreference := uint16(126)
	prflxDefaultPreference := uint16(110)
	srflxDefaultPreference := uint16(100)
	relayDefaultPreference := uint16(0)

	tcpOffsets := []uint16{0, 10}

	for _, tcpOffset := range tcpOffsets {
		agent := &Agent{
			tcpPriorityOffset: tcpOffset,
		}

		for _, networkType := range supportedNetworkTypes() {
			hostCandidate := candidateBase{
				candidateType: CandidateTypeHost,
				networkType:   networkType,
				currAgent:     agent,
			}
			prflxCandidate := candidateBase{
				candidateType: CandidateTypePeerReflexive,
				networkType:   networkType,
				currAgent:     agent,
			}
			srflxCandidate := candidateBase{
				candidateType: CandidateTypeServerReflexive,
				networkType:   networkType,
				currAgent:     agent,
			}
			relayCandidate := candidateBase{
				candidateType: CandidateTypeRelay,
				networkType:   networkType,
				currAgent:     agent,
			}

			if networkType.IsTCP() {
				req.Equal(hostDefaultPreference-tcpOffset, hostCandidate.TypePreference())
				req.Equal(prflxDefaultPreference-tcpOffset, prflxCandidate.TypePreference())
				req.Equal(srflxDefaultPreference-tcpOffset, srflxCandidate.TypePreference())
			} else {
				req.Equal(hostDefaultPreference, hostCandidate.TypePreference())
				req.Equal(prflxDefaultPreference, prflxCandidate.TypePreference())
				req.Equal(srflxDefaultPreference, srflxCandidate.TypePreference())
			}

			req.Equal(relayDefaultPreference, relayCandidate.TypePreference())
		}
	}
}

func TestCandidatePriority(t *testing.T) {
	for _, test := range []struct {
		Candidate    Candidate
		WantPriority uint32
	}{
		{
			Candidate: &CandidateHost{
				candidateBase: candidateBase{
					candidateType: CandidateTypeHost,
					component:     ComponentRTP,
				},
			},
			WantPriority: 2130706431,
		},
		{
			Candidate: &CandidateHost{
				candidateBase: candidateBase{
					candidateType: CandidateTypeHost,
					component:     ComponentRTP,
					networkType:   NetworkTypeTCP4,
					tcpType:       TCPTypeActive,
				},
			},
			WantPriority: 1675624447,
		},
		{
			Candidate: &CandidateHost{
				candidateBase: candidateBase{
					candidateType: CandidateTypeHost,
					component:     ComponentRTP,
					networkType:   NetworkTypeTCP4,
					tcpType:       TCPTypePassive,
				},
			},
			WantPriority: 1671430143,
		},
		{
			Candidate: &CandidateHost{
				candidateBase: candidateBase{
					candidateType: CandidateTypeHost,
					component:     ComponentRTP,
					networkType:   NetworkTypeTCP4,
					tcpType:       TCPTypeSimultaneousOpen,
				},
			},
			WantPriority: 1667235839,
		},
		{
			Candidate: &CandidatePeerReflexive{
				candidateBase: candidateBase{
					candidateType: CandidateTypePeerReflexive,
					component:     ComponentRTP,
				},
			},
			WantPriority: 1862270975,
		},
		{
			Candidate: &CandidatePeerReflexive{
				candidateBase: candidateBase{
					candidateType: CandidateTypePeerReflexive,
					component:     ComponentRTP,
					networkType:   NetworkTypeTCP6,
					tcpType:       TCPTypeSimultaneousOpen,
				},
			},
			WantPriority: 1407188991,
		},
		{
			Candidate: &CandidatePeerReflexive{
				candidateBase: candidateBase{
					candidateType: CandidateTypePeerReflexive,
					component:     ComponentRTP,
					networkType:   NetworkTypeTCP6,
					tcpType:       TCPTypeActive,
				},
			},
			WantPriority: 1402994687,
		},
		{
			Candidate: &CandidatePeerReflexive{
				candidateBase: candidateBase{
					candidateType: CandidateTypePeerReflexive,
					component:     ComponentRTP,
					networkType:   NetworkTypeTCP6,
					tcpType:       TCPTypePassive,
				},
			},
			WantPriority: 1398800383,
		},
		{
			Candidate: &CandidateServerReflexive{
				candidateBase: candidateBase{
					candidateType: CandidateTypeServerReflexive,
					component:     ComponentRTP,
				},
			},
			WantPriority: 1694498815,
		},
		{
			Candidate: &CandidateRelay{
				candidateBase: candidateBase{
					candidateType: CandidateTypeRelay,
					component:     ComponentRTP,
				},
			},
			WantPriority: 16777215,
		},
	} {
		require.Equal(t, test.Candidate.Priority(), test.WantPriority)
	}
}

func TestCandidateLastSent(t *testing.T) {
	candidate := candidateBase{}
	require.Equal(t, candidate.LastSent(), time.Time{})
	now := time.Now()
	candidate.setLastSent(now)
	require.Equal(t, candidate.LastSent(), now)
}

func TestCandidateLastReceived(t *testing.T) {
	candidate := candidateBase{}
	require.Equal(t, candidate.LastReceived(), time.Time{})
	now := time.Now()
	candidate.setLastReceived(now)
	require.Equal(t, candidate.LastReceived(), now)
}

func TestCandidateFoundation(t *testing.T) {
	// All fields are the same
	require.Equal(t,
		(&candidateBase{
			candidateType: CandidateTypeHost,
			networkType:   NetworkTypeUDP4,
			address:       "A",
		}).Foundation(),
		(&candidateBase{
			candidateType: CandidateTypeHost,
			networkType:   NetworkTypeUDP4,
			address:       "A",
		}).Foundation())

	// Different Address
	require.NotEqual(t,
		(&candidateBase{
			candidateType: CandidateTypeHost,
			networkType:   NetworkTypeUDP4,
			address:       "A",
		}).Foundation(),
		(&candidateBase{
			candidateType: CandidateTypeHost,
			networkType:   NetworkTypeUDP4,
			address:       "B",
		}).Foundation())

	// Different networkType
	require.NotEqual(t,
		(&candidateBase{
			candidateType: CandidateTypeHost,
			networkType:   NetworkTypeUDP4,
			address:       "A",
		}).Foundation(),
		(&candidateBase{
			candidateType: CandidateTypeHost,
			networkType:   NetworkTypeUDP6,
			address:       "A",
		}).Foundation())

	// Different candidateType
	require.NotEqual(t,
		(&candidateBase{
			candidateType: CandidateTypeHost,
			networkType:   NetworkTypeUDP4,
			address:       "A",
		}).Foundation(),
		(&candidateBase{
			candidateType: CandidateTypePeerReflexive,
			networkType:   NetworkTypeUDP4,
			address:       "A",
		}).Foundation())

	// Port has no effect
	require.Equal(t,
		(&candidateBase{
			candidateType: CandidateTypeHost,
			networkType:   NetworkTypeUDP4,
			address:       "A",
			port:          8080,
		}).Foundation(),
		(&candidateBase{
			candidateType: CandidateTypeHost,
			networkType:   NetworkTypeUDP4,
			address:       "A",
			port:          80,
		}).Foundation())
}

func mustCandidateHost(t *testing.T, conf *CandidateHostConfig) Candidate {
	t.Helper()

	cand, err := NewCandidateHost(conf)
	require.NoError(t, err)

	return cand
}

func mustCandidateHostWithExtensions(
	t *testing.T,
	conf *CandidateHostConfig,
	extensions []CandidateExtension,
) Candidate {
	t.Helper()

	cand, err := NewCandidateHost(conf)
	require.NoError(t, err)

	cand.setExtensions(extensions)

	return cand
}

func mustCandidateRelay(t *testing.T, conf *CandidateRelayConfig) Candidate {
	t.Helper()

	cand, err := NewCandidateRelay(conf)
	require.NoError(t, err)

	return cand
}

func mustCandidateRelayWithExtensions(
	t *testing.T,
	conf *CandidateRelayConfig,
	extensions []CandidateExtension,
) Candidate {
	t.Helper()

	cand, err := NewCandidateRelay(conf)
	require.NoError(t, err)

	cand.setExtensions(extensions)

	return cand
}

func mustCandidateServerReflexive(t *testing.T, conf *CandidateServerReflexiveConfig) Candidate {
	t.Helper()

	cand, err := NewCandidateServerReflexive(conf)
	require.NoError(t, err)

	return cand
}

func mustCandidateServerReflexiveWithExtensions(
	t *testing.T,
	conf *CandidateServerReflexiveConfig,
	extensions []CandidateExtension,
) Candidate {
	t.Helper()

	cand, err := NewCandidateServerReflexive(conf)
	require.NoError(t, err)

	cand.setExtensions(extensions)

	return cand
}

func mustCandidatePeerReflexiveWithExtensions(
	t *testing.T,
	conf *CandidatePeerReflexiveConfig,
	extensions []CandidateExtension,
) Candidate {
	t.Helper()

	cand, err := NewCandidatePeerReflexive(conf)
	require.NoError(t, err)

	cand.setExtensions(extensions)

	return cand
}

func TestCandidateMarshal(t *testing.T) {
	for idx, test := range []struct {
		candidate   Candidate
		marshaled   string
		expectError bool
	}{
		{
			mustCandidateHost(t, &CandidateHostConfig{
				Network:    NetworkTypeUDP6.String(),
				Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
				Port:       53987,
				Priority:   500,
				Foundation: "750",
			}),
			"750 1 udp 500 fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a 53987 typ host",
			false,
		},
		{
			mustCandidateHost(t, &CandidateHostConfig{
				Network: NetworkTypeUDP4.String(),
				Address: "10.0.75.1",
				Port:    53634,
			}),
			"4273957277 1 udp 2130706431 10.0.75.1 53634 typ host",
			false,
		},
		{
			mustCandidateServerReflexive(t, &CandidateServerReflexiveConfig{
				Network: NetworkTypeUDP4.String(),
				Address: "191.228.238.68",
				Port:    53991,
				RelAddr: "192.168.0.274",
				RelPort: 53991,
			}),
			"647372371 1 udp 1694498815 191.228.238.68 53991 typ srflx raddr 192.168.0.274 rport 53991",
			false,
		},
		{
			mustCandidatePeerReflexiveWithExtensions(
				t,
				&CandidatePeerReflexiveConfig{
					Network: NetworkTypeTCP4.String(),
					Address: "192.0.2.15",
					Port:    50000,
					RelAddr: "10.0.0.1",
					RelPort: 12345,
				},
				[]CandidateExtension{
					{"generation", "0"},
					{"network-id", "2"},
					{"network-cost", "10"},
				},
			),
			//nolint: lll
			"4207374052 1 tcp 1685790463 192.0.2.15 50000 typ prflx raddr 10.0.0.1 rport 12345 generation 0 network-id 2 network-cost 10",
			false,
		},
		{
			mustCandidateRelay(t, &CandidateRelayConfig{
				Network: NetworkTypeUDP4.String(),
				Address: "50.0.0.1",
				Port:    5000,
				RelAddr: "192.168.0.1",
				RelPort: 5001,
			}),
			"848194626 1 udp 16777215 50.0.0.1 5000 typ relay raddr 192.168.0.1 rport 5001",
			false,
		},
		{
			mustCandidateHost(t, &CandidateHostConfig{
				Network: NetworkTypeTCP4.String(),
				Address: "192.168.0.196",
				Port:    0,
				TCPType: TCPTypeActive,
			}),
			"1052353102 1 tcp 2128609279 192.168.0.196 0 typ host tcptype active",
			false,
		},
		{
			mustCandidateHost(t, &CandidateHostConfig{
				Network: NetworkTypeUDP4.String(),
				Address: "e2494022-4d9a-4c1e-a750-cc48d4f8d6ee.local",
				Port:    60542,
			}),
			"1380287402 1 udp 2130706431 e2494022-4d9a-4c1e-a750-cc48d4f8d6ee.local 60542 typ host", false,
		},
		// Missing Foundation
		{
			mustCandidateHost(t, &CandidateHostConfig{
				Network:    NetworkTypeUDP4.String(),
				Address:    localhostIPStr,
				Port:       80,
				Priority:   500,
				Foundation: " ",
			}),
			" 1 udp 500 " + localhostIPStr + " 80 typ host",
			false,
		},
		// Missing Foundation
		{
			mustCandidateHost(t, &CandidateHostConfig{
				Network:    NetworkTypeUDP4.String(),
				Address:    localhostIPStr,
				Port:       80,
				Priority:   500,
				Foundation: " ",
			}),
			"candidate: 1 udp 500 " + localhostIPStr + " 80 typ host",
			false,
		},
		{
			mustCandidateHost(t, &CandidateHostConfig{
				Network:    NetworkTypeUDP4.String(),
				Address:    localhostIPStr,
				Port:       80,
				Priority:   500,
				Foundation: "+/3713fhi",
			}),
			"+/3713fhi 1 udp 500 " + localhostIPStr + " 80 typ host",
			false,
		},
		{
			mustCandidateHost(t, &CandidateHostConfig{
				Network:    NetworkTypeTCP4.String(),
				Address:    "172.28.142.173",
				Port:       7686,
				Priority:   1671430143,
				Foundation: "+/3713fhi",
			}),
			"3359356140 1 tcp 1671430143 172.28.142.173 7686 typ host",
			false,
		},
		{
			mustCandidateHost(t, &CandidateHostConfig{
				Network:    NetworkTypeTCP4.String(),
				Address:    "172.28.142.173",
				Port:       7686,
				Priority:   1671430143,
				Foundation: "+/3713fhi",
			}),
			"candidate:3359356140 1 tcp 1671430143 172.28.142.173 7686 typ host",
			false,
		},

		// Invalid candidates
		{nil, "", true},
		{nil, "1938809241", true},
		{nil, "1986380506 99999999 udp 2122063615 10.0.75.1 53634 typ host generation 0 network-id 2", true},
		{nil, "1986380506 1 udp 99999999999 10.0.75.1 53634 typ host", true},
		//nolint: lll
		{nil, "4207374051 1 udp 1685790463 191.228.238.68 99999999 typ srflx raddr 192.168.0.278 rport 53991 generation 0 network-id 3", true},
		{nil, "4207374051 1 udp 1685790463 191.228.238.68 53991 typ srflx raddr", true},
		//nolint: lll
		{nil, "4207374051 1 udp 1685790463 191.228.238.68 53991 typ srflx raddr 192.168.0.278 rport 99999999 generation 0 network-id 3", true},
		{nil, "4207374051 INVALID udp 2130706431 10.0.75.1 53634 typ host", true},
		{nil, "4207374051 1 udp INVALID 10.0.75.1 53634 typ host", true},
		{nil, "4207374051 INVALID udp 2130706431 10.0.75.1 INVALID typ host", true},
		{nil, "4207374051 1 udp 2130706431 10.0.75.1 53634 typ INVALID", true},
		{nil, "4207374051 1 INVALID 2130706431 10.0.75.1 53634 typ host", true},
		{nil, "4207374051 1 INVALID 2130706431 10.0.75.1 53634 typ", true},
		{nil, "4207374051 1 INVALID 2130706431 10.0.75.1 53634", true},
		{nil, "848194626 1 udp 16777215 50.0.0.^^1 5000 typ relay raddr 192.168.0.1 rport 5001", true},
		{nil, "4207374052 1 tcp 1685790463 192.0#.2.15 50000 typ prflx raddr 10.0.0.1 rport 12345 rport 5001", true},
		{nil, "647372371 1 udp 1694498815 191.228.2@338.68 53991 typ srflx raddr 192.168.0.274 rport 53991", true},
		// invalid foundion; longer than 32 characters
		{nil, "111111111111111111111111111111111 1 udp 500 " + localhostIPStr + " 80 typ host", true},
		// Invalid ice-char
		{nil, "3$3 1 udp 500 " + localhostIPStr + " 80 typ host", true},
		// invalid component; longer than 5 digits
		{nil, "4207374051 123456 udp 500 " + localhostIPStr + " 0 typ host", true},
		// invalid priority; longer than 10 digits
		{nil, "4207374051 99999 udp 12345678910 " + localhostIPStr + " 99999 typ host", true},
		// invalid port;
		{nil, "4207374051 99999 udp 500 " + localhostIPStr + " 65536 typ host", true},
		{nil, "4207374051 99999 udp 500 " + localhostIPStr + " 999999 typ host", true},
		{nil, "848194626 1 udp 16777215 50.0.0.1 5000 typ relay raddr 192.168.0.1 rport 999999", true},

		// bad byte-string in extension value
		{nil, "750 1 udp 500 fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a 53987 typ host ext valu\nu", true},
		{nil, "848194626 1 udp 16777215 50.0.0.1 5000 typ relay raddr 192.168.0.1 rport 654 ext valu\nu", true},
		{nil, "848194626 1 udp 16777215 50.0.0.1 5000 typ relay raddr 192.168.0.1 rport 654 ext valu\000e", true},

		// bad byte-string in extension key
		{nil, "848194626 1 udp 16777215 50.0.0.1 5000 typ relay raddr 192.168.0.1 rport 654 ext\r value", true},

		// invalid tcptype
		{nil, "1052353102 1 tcp 2128609279 192.168.0.196 0 typ host tcptype INVALID", true},

		// expect rport after raddr
		{nil, "848194626 1 udp 16777215 50.0.0.1 5000 typ relay raddr 192.168.0.1 extension 322", true},
		{nil, "848194626 1 udp 16777215 50.0.0.1 5000 typ relay raddr 192.168.0.1 rport", true},
		{nil, "848194626 1 udp 16777215 50.0.0.1 5000 typ relay raddr 192.168.0.1", true},
		{nil, "848194626 1 udp 16777215 50.0.0.1 5000 typ relay raddr", true},
		{nil, "4207374051 99999 udp 500 " + localhostIPStr + " 80 typ", true},
		{nil, "4207374051 99999 udp 500 " + localhostIPStr + " 80", true},
		{nil, "4207374051 99999 udp 500 " + localhostIPStr, true},
		{nil, "4207374051 99999 udp 500 ", true},
		{nil, "4207374051 99999 udp", true},
		{nil, "4207374051 99999", true},
		{nil, "4207374051", true},
	} {
		t.Run(strconv.Itoa(idx), func(t *testing.T) {
			actualCandidate, err := UnmarshalCandidate(test.marshaled)
			if test.expectError {
				require.Error(t, err, "expected error", test.marshaled)

				return
			}

			require.NoError(t, err)

			require.Truef(
				t,
				test.candidate.Equal(actualCandidate),
				"%s != %s",
				test.candidate.String(),
				actualCandidate.String(),
			)

			if strings.HasPrefix(test.marshaled, "candidate:") {
				require.Equal(t, test.marshaled[len("candidate:"):], actualCandidate.Marshal())
			} else {
				require.Equal(t, test.marshaled, actualCandidate.Marshal())
			}
		})
	}
}

func TestCandidateWriteTo(t *testing.T) {
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP:   net.IP{127, 0, 0, 1},
		Port: 0,
	})
	require.NoError(t, err, "error creating test TCP listener")

	conn, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr)) // nolint
	require.NoError(t, err, "error dialing test TCP connection")

	loggerFactory := logging.NewDefaultLoggerFactory()
	packetConn := newTCPPacketConn(tcpPacketParams{
		ReadBuffer: 2048,
		Logger:     loggerFactory.NewLogger("tcp-packet-conn"),
	})

	err = packetConn.AddConn(conn, nil)
	require.NoError(t, err, "error adding test TCP connection to packet connection")

	c1 := &candidateBase{
		conn: packetConn,
		currAgent: &Agent{
			log: loggerFactory.NewLogger("agent"),
		},
	}

	c2 := &candidateBase{
		resolvedAddr: listener.Addr(),
	}

	_, err = c1.writeTo([]byte("test"), c2)
	require.NoError(t, err, "writing to open conn")

	err = packetConn.Close()
	require.NoError(t, err, "error closing test TCP connection")

	_, err = c1.writeTo([]byte("test"), c2)
	require.Error(t, err, "writing to closed conn")
}

func TestMarshalUnmarshalCandidateWithZoneID(t *testing.T) {
	candidateWithZoneID := mustCandidateHost(t, &CandidateHostConfig{
		Network:    NetworkTypeUDP6.String(),
		Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a%Local Connection",
		Port:       53987,
		Priority:   500,
		Foundation: "750",
	})
	candidateStr := "750 0 udp 500 fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a 53987 typ host"
	require.Equal(t, candidateStr, candidateWithZoneID.Marshal())

	candidate := mustCandidateHost(t, &CandidateHostConfig{
		Network:    NetworkTypeUDP6.String(),
		Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
		Port:       53987,
		Priority:   500,
		Foundation: "750",
	})
	candidateWithZoneIDStr := "750 0 udp 500 fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a%eth0 53987 typ host"
	candidate2, err := UnmarshalCandidate(candidateWithZoneIDStr)
	require.NoError(t, err)
	require.Truef(t, candidate.Equal(candidate2), "%s != %s", candidate.String(), candidate2.String())

	candidateWithZoneIDStr2 := "750 0 udp 500 fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a%eth0%eth1 53987 typ host"
	candidate2, err = UnmarshalCandidate(candidateWithZoneIDStr2)
	require.NoError(t, err)
	require.Truef(t, candidate.Equal(candidate2), "%s != %s", candidate.String(), candidate2.String())
}

func TestCandidateExtensionsMarshal(t *testing.T) {
	testCases := []struct {
		Extensions []CandidateExtension
		candidate  string
	}{
		{
			[]CandidateExtension{
				{"generation", "0"},
				{"ufrag", "QNvE"},
				{"network-id", "4"},
			},
			//nolint: lll
			"1299692247 1 udp 2122134271 fdc8:cc8:c835:e400:343c:feb:32c8:17b9 58240 typ host generation 0 ufrag QNvE network-id 4",
		},
		{
			[]CandidateExtension{
				{"generation", "1"},
				{"network-id", "2"},
				{"network-cost", "50"},
			},
			//nolint:lll
			"647372371 1 udp 1694498815 191.228.238.68 53991 typ srflx raddr 192.168.0.274 rport 53991 generation 1 network-id 2 network-cost 50",
		},
		{
			[]CandidateExtension{
				{"generation", "0"},
				{"network-id", "2"},
				{"network-cost", "10"},
			},
			//nolint:lll
			"4207374052 1 tcp 1685790463 192.0.2.15 50000 typ prflx raddr 10.0.0.1 rport 12345 generation 0 network-id 2 network-cost 10",
		},
		{
			[]CandidateExtension{
				{"generation", "0"},
				{"network-id", "1"},
				{"network-cost", "20"},
				{"ufrag", "frag42abcdef"},
				{"password", "abc123exp123"},
			},
			//nolint: lll
			"848194626 1 udp 16777215 50.0.0.1 5000 typ relay raddr 192.168.0.1 rport 5001 generation 0 network-id 1 network-cost 20 ufrag frag42abcdef password abc123exp123",
		},
		{
			[]CandidateExtension{
				{"tcptype", "active"},
				{"generation", "0"},
			},
			"1052353102 1 tcp 2128609279 192.168.0.196 0 typ host tcptype active generation 0",
		},
		{
			[]CandidateExtension{
				{"tcptype", "active"},
				{"generation", "0"},
			},
			"1052353102 1 tcp 2128609279 192.168.0.196 0 typ host tcptype active generation 0",
		},
		{
			[]CandidateExtension{},
			"1052353102 1 tcp 2128609279 192.168.0.196 0 typ host",
		},
		{
			[]CandidateExtension{
				{"tcptype", "active"},
				{"empty-value-1", ""},
				{"empty-value-2", ""},
			},
			"1052353102 1 tcp 2128609279 192.168.0.196 0 typ host tcptype active empty-value-1  empty-value-2",
		},
		{
			[]CandidateExtension{
				{"tcptype", "active"},
				{"empty-value-1", ""},
				{"empty-value-2", ""},
			},
			"1052353102 1 tcp 2128609279 192.168.0.196 0 typ host tcptype active empty-value-1  empty-value-2 ",
		},
	}

	for _, tc := range testCases {
		candidate, err := UnmarshalCandidate(tc.candidate)
		require.NoError(t, err)
		require.Equal(t, tc.Extensions, candidate.Extensions(), "Extensions should be equal", tc.candidate)

		valueStr := candidate.Marshal()
		candidate2, err := UnmarshalCandidate(valueStr)

		require.NoError(t, err)
		require.Equal(t, tc.Extensions, candidate2.Extensions(), "Marshal() should preserve extensions")
	}
}

func TestCandidateExtensionsDeepEqual(t *testing.T) {
	noExt, err := UnmarshalCandidate("750 0 udp 500 fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a 53987 typ host")
	require.NoError(t, err)

	generation := "0"
	ufrag := "QNvE"
	networkID := "4"

	extensions := []CandidateExtension{
		{"generation", generation},
		{"ufrag", ufrag},
		{"network-id", networkID},
	}

	candidate, err := UnmarshalCandidate(
		"750 0 udp 500 fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a 53987 typ host generation " +
			generation + " ufrag " + ufrag + " network-id " + networkID,
	)
	require.NoError(t, err)

	testCases := []struct {
		a     Candidate
		b     Candidate
		equal bool
	}{
		{
			mustCandidateHost(t, &CandidateHostConfig{
				Network:    NetworkTypeUDP4.String(),
				Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
				Port:       53987,
				Priority:   500,
				Foundation: "750",
			}),
			noExt,
			true,
		},
		{
			mustCandidateHostWithExtensions(
				t,
				&CandidateHostConfig{
					Network:    NetworkTypeUDP4.String(),
					Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
					Port:       53987,
					Priority:   500,
					Foundation: "750",
				},
				[]CandidateExtension{},
			),
			noExt,
			true,
		},
		{
			mustCandidateHostWithExtensions(
				t,
				&CandidateHostConfig{
					Network:    NetworkTypeUDP4.String(),
					Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
					Port:       53987,
					Priority:   500,
					Foundation: "750",
				},
				extensions,
			),
			candidate,
			true,
		},
		{
			mustCandidateRelayWithExtensions(
				t,
				&CandidateRelayConfig{
					Network: NetworkTypeUDP4.String(),
					Address: "10.0.0.10",
					Port:    5000,
					RelAddr: "10.0.0.2",
					RelPort: 5001,
				},
				[]CandidateExtension{
					{"generation", "0"},
					{"network-id", "1"},
				},
			),
			mustCandidateRelayWithExtensions(
				t,
				&CandidateRelayConfig{
					Network: NetworkTypeUDP4.String(),
					Address: "10.0.0.10",
					Port:    5000,
					RelAddr: "10.0.0.2",
					RelPort: 5001,
				},
				[]CandidateExtension{
					{"network-id", "1"},
					{"generation", "0"},
				},
			),
			true,
		},
		{
			mustCandidatePeerReflexiveWithExtensions(
				t,
				&CandidatePeerReflexiveConfig{
					Network: NetworkTypeTCP4.String(),
					Address: "192.0.2.15",
					Port:    50000,
					RelAddr: "10.0.0.1",
					RelPort: 12345,
				},
				[]CandidateExtension{
					{"generation", "0"},
					{"network-id", "2"},
					{"network-cost", "10"},
				},
			),
			mustCandidatePeerReflexiveWithExtensions(
				t,
				&CandidatePeerReflexiveConfig{
					Network: NetworkTypeTCP4.String(),
					Address: "192.0.2.15",
					Port:    50000,
					RelAddr: "10.0.0.1",
					RelPort: 12345,
				},
				[]CandidateExtension{
					{"generation", "0"},
					{"network-id", "2"},
					{"network-cost", "10"},
				},
			),
			true,
		},
		{
			mustCandidateServerReflexiveWithExtensions(
				t,
				&CandidateServerReflexiveConfig{
					Network: NetworkTypeUDP4.String(),
					Address: "191.228.238.68",
					Port:    53991,
					RelAddr: "192.168.0.274",
					RelPort: 53991,
				},
				[]CandidateExtension{
					{"generation", "0"},
					{"network-id", "2"},
					{"network-cost", "10"},
				},
			),
			mustCandidateServerReflexiveWithExtensions(
				t,
				&CandidateServerReflexiveConfig{
					Network: NetworkTypeUDP4.String(),
					Address: "191.228.238.68",
					Port:    53991,
					RelAddr: "192.168.0.274",
					RelPort: 53991,
				},
				[]CandidateExtension{
					{"generation", "0"},
					{"network-id", "2"},
					{"network-cost", "10"},
				},
			),
			true,
		},
		{
			mustCandidateHostWithExtensions(
				t,
				&CandidateHostConfig{
					Network:    NetworkTypeUDP4.String(),
					Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
					Port:       53987,
					Priority:   500,
					Foundation: "750",
				},
				[]CandidateExtension{
					{"generation", "5"},
					{"ufrag", ufrag},
					{"network-id", networkID},
				},
			),
			candidate,
			false,
		},
		{
			mustCandidateHostWithExtensions(
				t,
				&CandidateHostConfig{
					Network:    NetworkTypeTCP4.String(),
					Address:    "192.168.0.196",
					Port:       0,
					Priority:   2128609279,
					Foundation: "1052353102",
					TCPType:    TCPTypeActive,
				},
				[]CandidateExtension{
					{"tcptype", TCPTypeActive.String()},
					{"generation", "0"},
				},
			),
			mustCandidateHostWithExtensions(
				t,
				&CandidateHostConfig{
					Network:    NetworkTypeTCP4.String(),
					Address:    "192.168.0.197",
					Port:       0,
					Priority:   2128609279,
					Foundation: "1052353102",
					TCPType:    TCPTypeActive,
				},
				[]CandidateExtension{
					{"tcptype", TCPTypeActive.String()},
					{"generation", "0"},
				},
			),
			false,
		},
	}

	for _, tc := range testCases {
		require.Equal(t, tc.a.DeepEqual(tc.b), tc.equal, "a: %s, b: %s", tc.a.Marshal(), tc.b.Marshal())
	}
}

func TestUnmarshalCandidateExtensions(t *testing.T) {
	testCases := []struct {
		name     string
		value    string
		expected []CandidateExtension
		fail     bool
	}{
		{
			name:     "empty string",
			value:    "",
			expected: []CandidateExtension{},
			fail:     false,
		},
		{
			name:     "valid extension string",
			value:    "a b c d",
			expected: []CandidateExtension{{"a", "b"}, {"c", "d"}},
			fail:     false,
		},
		{
			name:  "valid extension string",
			value: "a b empty  c d",
			expected: []CandidateExtension{
				{"a", "b"},
				{"empty", ""},
				{"c", "d"},
			},
			fail: false,
		},
		{
			name:     "invalid extension",
			value:    " a b d",
			expected: []CandidateExtension{{"", "a"}, {"b", "d"}},
			fail:     true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			req := require.New(t)

			actual, _, err := unmarshalCandidateExtensions(testCase.value)
			if testCase.fail {
				req.Error(err)
			} else {
				req.NoError(err)
				req.EqualValuesf(
					testCase.expected,
					actual,
					"UnmarshalCandidateExtensions() did not return the expected value %v",
					testCase.value,
				)
			}
		})
	}
}

func TestCandidateGetExtension(t *testing.T) {
	t.Run("Get extension", func(t *testing.T) {
		extensions := []CandidateExtension{
			{"a", "b"},
			{"c", "d"},
		}
		candidate, err := NewCandidateHost(&CandidateHostConfig{
			Network:    NetworkTypeUDP4.String(),
			Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
			Port:       53987,
			Priority:   500,
			Foundation: "750",
		})
		require.NoError(t, err)

		candidate.setExtensions(extensions)

		value, ok := candidate.GetExtension("c")
		require.True(t, ok)
		require.Equal(t, "c", value.Key)
		require.Equal(t, "d", value.Value)

		value, ok = candidate.GetExtension("a")
		require.True(t, ok)
		require.Equal(t, "a", value.Key)
		require.Equal(t, "b", value.Value)

		value, ok = candidate.GetExtension("b")
		require.False(t, ok)
		require.Equal(t, "b", value.Key)
		require.Equal(t, "", value.Value)
	})

	// This is undefined behavior in the spec; extension-att-name is not unique
	// but it implied that it's unique in the implementation
	t.Run("Extension with multiple values", func(t *testing.T) {
		extensions := []CandidateExtension{
			{"a", "1"},
			{"a", "2"},
		}

		candidate, err := NewCandidateHost(&CandidateHostConfig{
			Network:    NetworkTypeUDP4.String(),
			Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
			Port:       53987,
			Priority:   500,
			Foundation: "750",
		})
		require.NoError(t, err)

		candidate.setExtensions(extensions)

		value, ok := candidate.GetExtension("a")
		require.True(t, ok)
		require.Equal(t, "a", value.Key)
		require.Equal(t, "1", value.Value)
	})

	t.Run("TCPType extension", func(t *testing.T) {
		extensions := []CandidateExtension{
			{"tcptype", "passive"},
		}

		candidate, err := NewCandidateHost(&CandidateHostConfig{
			Network:    NetworkTypeTCP4.String(),
			Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
			Port:       53987,
			Priority:   500,
			Foundation: "750",
			TCPType:    TCPTypeActive,
		})
		require.NoError(t, err)

		tcpType, ok := candidate.GetExtension("tcptype")

		require.True(t, ok)
		require.Equal(t, "tcptype", tcpType.Key)
		require.Equal(t, TCPTypeActive.String(), tcpType.Value)

		candidate.setExtensions(extensions)

		tcpType, ok = candidate.GetExtension("tcptype")

		require.True(t, ok)
		require.Equal(t, "tcptype", tcpType.Key)
		require.Equal(t, "passive", tcpType.Value)

		candidate2, err := NewCandidateHost(&CandidateHostConfig{
			Network:    NetworkTypeTCP4.String(),
			Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
			Port:       53987,
			Priority:   500,
			Foundation: "750",
		})
		require.NoError(t, err)

		tcpType, ok = candidate2.GetExtension("tcptype")

		require.False(t, ok)
		require.Equal(t, "tcptype", tcpType.Key)
		require.Equal(t, "", tcpType.Value)
	})
}

func TestBaseCandidateMarshalExtensions(t *testing.T) {
	t.Run("Marshal extension", func(t *testing.T) {
		extensions := []CandidateExtension{
			{"generation", "0"},
			{"ValuE", "KeE"},
			{"empty", ""},
			{"another", "value"},
		}
		candidate, err := NewCandidateHost(&CandidateHostConfig{
			Network:    NetworkTypeUDP4.String(),
			Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
			Port:       53987,
			Priority:   500,
			Foundation: "750",
		})
		require.NoError(t, err)

		candidate.setExtensions(extensions)

		value := candidate.marshalExtensions()
		require.Equal(t, "generation 0 ValuE KeE empty  another value", value)
	})

	t.Run("Marshal Empty", func(t *testing.T) {
		candidate, err := NewCandidateHost(&CandidateHostConfig{
			Network:    NetworkTypeUDP4.String(),
			Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
			Port:       53987,
			Priority:   500,
			Foundation: "750",
		})
		require.NoError(t, err)

		value := candidate.marshalExtensions()
		require.Equal(t, "", value)
	})

	t.Run("Marshal TCPType no extension", func(t *testing.T) {
		candidate, err := NewCandidateHost(&CandidateHostConfig{
			Network:    NetworkTypeUDP4.String(),
			Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
			Port:       53987,
			Priority:   500,
			Foundation: "750",
			TCPType:    TCPTypeActive,
		})
		require.NoError(t, err)

		value := candidate.marshalExtensions()
		require.Equal(t, "tcptype active", value)
	})
}

func TestBaseCandidateExtensionsEqual(t *testing.T) {
	testCases := []struct {
		name        string
		extensions1 []CandidateExtension
		extensions2 []CandidateExtension
		expected    bool
	}{
		{
			name:        "Empty extensions",
			extensions1: []CandidateExtension{},
			extensions2: []CandidateExtension{},
			expected:    true,
		},
		{
			name:        "Single value extensions",
			extensions1: []CandidateExtension{{"a", "b"}},
			extensions2: []CandidateExtension{{"a", "b"}},
			expected:    true,
		},
		{
			name: "multiple value extensions",
			extensions1: []CandidateExtension{
				{"a", "b"},
				{"c", "d"},
			},
			extensions2: []CandidateExtension{
				{"a", "b"},
				{"c", "d"},
			},
			expected: true,
		},
		{
			name: "unsorted extensions",
			extensions1: []CandidateExtension{
				{"c", "d"},
				{"a", "b"},
			},
			extensions2: []CandidateExtension{
				{"a", "b"},
				{"c", "d"},
			},
			expected: true,
		},
		{
			name: "different values",
			extensions1: []CandidateExtension{
				{"a", "b"},
				{"c", "d"},
			},
			extensions2: []CandidateExtension{
				{"a", "b"},
				{"c", "e"},
			},
			expected: false,
		},
		{
			name: "different size",
			extensions1: []CandidateExtension{
				{"a", "b"},
				{"c", "d"},
			},
			extensions2: []CandidateExtension{
				{"a", "b"},
			},
			expected: false,
		},
		{
			name: "different keys",
			extensions1: []CandidateExtension{
				{"a", "b"},
				{"c", "d"},
			},
			extensions2: []CandidateExtension{
				{"a", "b"},
				{"e", "d"},
			},
			expected: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			cand, err := NewCandidateHost(&CandidateHostConfig{
				Network:    NetworkTypeUDP4.String(),
				Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
				Port:       53987,
				Priority:   500,
				Foundation: "750",
			})
			require.NoError(t, err)

			cand.setExtensions(testCase.extensions1)

			require.Equal(t, testCase.expected, cand.extensionsEqual(testCase.extensions2))
		})
	}
}

func TestCandidateAddExtension(t *testing.T) {
	t.Run("Add extension", func(t *testing.T) {
		candidate, err := NewCandidateHost(&CandidateHostConfig{
			Network:    NetworkTypeUDP4.String(),
			Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
			Port:       53987,
			Priority:   500,
			Foundation: "750",
		})
		require.NoError(t, err)

		require.NoError(t, candidate.AddExtension(CandidateExtension{"a", "b"}))
		require.NoError(t, candidate.AddExtension(CandidateExtension{"c", "d"}))

		extensions := candidate.Extensions()
		require.Equal(t, []CandidateExtension{{"a", "b"}, {"c", "d"}}, extensions)
	})

	t.Run("Add extension with existing key", func(t *testing.T) {
		candidate, err := NewCandidateHost(&CandidateHostConfig{
			Network:    NetworkTypeUDP4.String(),
			Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
			Port:       53987,
			Priority:   500,
			Foundation: "750",
		})
		require.NoError(t, err)

		require.NoError(t, candidate.AddExtension(CandidateExtension{"a", "b"}))
		require.NoError(t, candidate.AddExtension(CandidateExtension{"a", "d"}))

		extensions := candidate.Extensions()
		require.Equal(t, []CandidateExtension{{"a", "d"}}, extensions)
	})

	t.Run("Keep tcptype extension", func(t *testing.T) {
		candidate, err := NewCandidateHost(&CandidateHostConfig{
			Network:    NetworkTypeTCP4.String(),
			Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
			Port:       53987,
			Priority:   500,
			Foundation: "750",
			TCPType:    TCPTypeActive,
		})
		require.NoError(t, err)

		ext, ok := candidate.GetExtension("tcptype")
		require.True(t, ok)
		require.Equal(t, ext, CandidateExtension{"tcptype", "active"})
		require.Equal(t, candidate.Extensions(), []CandidateExtension{{"tcptype", "active"}})

		require.NoError(t, candidate.AddExtension(CandidateExtension{"a", "b"}))

		ext, ok = candidate.GetExtension("tcptype")
		require.True(t, ok)
		require.Equal(t, ext, CandidateExtension{"tcptype", "active"})
		require.Equal(t, candidate.Extensions(), []CandidateExtension{{"tcptype", "active"}, {"a", "b"}})
	})

	t.Run("TcpType change extension", func(t *testing.T) {
		candidate, err := NewCandidateHost(&CandidateHostConfig{
			Network:    NetworkTypeTCP4.String(),
			Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
			Port:       53987,
			Priority:   500,
			Foundation: "750",
		})
		require.NoError(t, err)

		require.NoError(t, candidate.AddExtension(CandidateExtension{"tcptype", "active"}))

		extensions := candidate.Extensions()
		require.Equal(t, []CandidateExtension{{"tcptype", "active"}}, extensions)
		require.Equal(t, TCPTypeActive, candidate.TCPType())

		require.Error(t, candidate.AddExtension(CandidateExtension{"tcptype", "INVALID"}))
	})

	t.Run("Add empty extension", func(t *testing.T) {
		candidate, err := NewCandidateHost(&CandidateHostConfig{
			Network:    NetworkTypeUDP4.String(),
			Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
			Port:       53987,
			Priority:   500,
			Foundation: "750",
		})
		require.NoError(t, err)

		require.Error(t, candidate.AddExtension(CandidateExtension{"", ""}))

		require.NoError(t, candidate.AddExtension(CandidateExtension{"a", ""}))

		extensions := candidate.Extensions()

		require.Equal(t, []CandidateExtension{{"a", ""}}, extensions)
	})
}

func TestCandidateRemoveExtension(t *testing.T) {
	t.Run("Remove extension", func(t *testing.T) {
		candidate, err := NewCandidateHost(&CandidateHostConfig{
			Network:    NetworkTypeUDP4.String(),
			Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
			Port:       53987,
			Priority:   500,
			Foundation: "750",
		})
		require.NoError(t, err)

		require.NoError(t, candidate.AddExtension(CandidateExtension{"a", "b"}))
		require.NoError(t, candidate.AddExtension(CandidateExtension{"c", "d"}))

		require.True(t, candidate.RemoveExtension("a"))

		extensions := candidate.Extensions()
		require.Equal(t, []CandidateExtension{{"c", "d"}}, extensions)
	})

	t.Run("Remove extension that does not exist", func(t *testing.T) {
		candidate, err := NewCandidateHost(&CandidateHostConfig{
			Network:    NetworkTypeUDP4.String(),
			Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
			Port:       53987,
			Priority:   500,
			Foundation: "750",
		})
		require.NoError(t, err)

		require.NoError(t, candidate.AddExtension(CandidateExtension{"a", "b"}))
		require.NoError(t, candidate.AddExtension(CandidateExtension{"c", "d"}))

		require.False(t, candidate.RemoveExtension("b"))

		extensions := candidate.Extensions()
		require.Equal(t, []CandidateExtension{{"a", "b"}, {"c", "d"}}, extensions)
	})

	t.Run("Remove tcptype extension", func(t *testing.T) {
		candidate, err := NewCandidateHost(&CandidateHostConfig{
			Network:    NetworkTypeTCP4.String(),
			Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
			Port:       53987,
			Priority:   500,
			Foundation: "750",
			TCPType:    TCPTypeActive,
		})
		require.NoError(t, err)

		// tcptype extension should be removed, even if it's not in the extensions list (Not Parsed)
		require.True(t, candidate.RemoveExtension("tcptype"))
		require.Equal(t, TCPTypeUnspecified, candidate.TCPType())
		require.Empty(t, candidate.Extensions())

		require.NoError(t, candidate.AddExtension(CandidateExtension{"tcptype", "passive"}))

		require.True(t, candidate.RemoveExtension("tcptype"))
		require.Equal(t, TCPTypeUnspecified, candidate.TCPType())
		require.Empty(t, candidate.Extensions())
	})
}
