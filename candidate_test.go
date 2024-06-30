// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/stretchr/testify/require"
)

const localhostIPStr = "127.0.0.1"

func TestCandidateTypePreference(t *testing.T) {
	r := require.New(t)

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
				r.Equal(hostDefaultPreference-tcpOffset, hostCandidate.TypePreference())
				r.Equal(prflxDefaultPreference-tcpOffset, prflxCandidate.TypePreference())
				r.Equal(srflxDefaultPreference-tcpOffset, srflxCandidate.TypePreference())
			} else {
				r.Equal(hostDefaultPreference, hostCandidate.TypePreference())
				r.Equal(prflxDefaultPreference, prflxCandidate.TypePreference())
				r.Equal(srflxDefaultPreference, srflxCandidate.TypePreference())
			}

			r.Equal(relayDefaultPreference, relayCandidate.TypePreference())
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
		if got, want := test.Candidate.Priority(), test.WantPriority; got != want {
			t.Fatalf("Candidate(%v).Priority() = %d, want %d", test.Candidate, got, want)
		}
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

func mustCandidateHost(conf *CandidateHostConfig) Candidate {
	cand, err := NewCandidateHost(conf)
	if err != nil {
		panic(err)
	}
	return cand
}

func mustCandidateRelay(conf *CandidateRelayConfig) Candidate {
	cand, err := NewCandidateRelay(conf)
	if err != nil {
		panic(err)
	}
	return cand
}

func mustCandidateServerReflexive(conf *CandidateServerReflexiveConfig) Candidate {
	cand, err := NewCandidateServerReflexive(conf)
	if err != nil {
		panic(err)
	}
	return cand
}

func TestCandidateMarshal(t *testing.T) {
	for idx, test := range []struct {
		candidate   Candidate
		marshaled   string
		expectError bool
	}{
		{
			mustCandidateHost(&CandidateHostConfig{
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
			mustCandidateHost(&CandidateHostConfig{
				Network:    NetworkTypeUDP6.String(),
				Address:    "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a%Local connection",
				Port:       53987,
				Priority:   500,
				Foundation: "750",
			}),
			"750 1 udp 500 fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a%Local%20connection 53987 typ host",
			false,
		},
		{
			mustCandidateHost(&CandidateHostConfig{
				Network: NetworkTypeUDP4.String(),
				Address: "10.0.75.1",
				Port:    53634,
			}),
			"4273957277 1 udp 2130706431 10.0.75.1 53634 typ host",
			false,
		},
		{
			mustCandidateServerReflexive(&CandidateServerReflexiveConfig{
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
			mustCandidateRelay(&CandidateRelayConfig{
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
			mustCandidateHost(&CandidateHostConfig{
				Network: NetworkTypeTCP4.String(),
				Address: "192.168.0.196",
				Port:    0,
				TCPType: TCPTypeActive,
			}),
			"1052353102 1 tcp 2128609279 192.168.0.196 0 typ host tcptype active",
			false,
		},
		{
			mustCandidateHost(&CandidateHostConfig{
				Network: NetworkTypeUDP4.String(),
				Address: "e2494022-4d9a-4c1e-a750-cc48d4f8d6ee.local",
				Port:    60542,
			}),
			"1380287402 1 udp 2130706431 e2494022-4d9a-4c1e-a750-cc48d4f8d6ee.local 60542 typ host", false,
		},
		// Missing Foundation
		{
			mustCandidateHost(&CandidateHostConfig{
				Network:    NetworkTypeUDP4.String(),
				Address:    localhostIPStr,
				Port:       80,
				Priority:   500,
				Foundation: " ",
			}),
			" 1 udp 500 " + localhostIPStr + " 80 typ host",
			false,
		},

		// Invalid candidates
		{nil, "", true},
		{nil, "1938809241", true},
		{nil, "1986380506 99999999 udp 2122063615 10.0.75.1 53634 typ host generation 0 network-id 2", true},
		{nil, "1986380506 1 udp 99999999999 10.0.75.1 53634 typ host", true},
		{nil, "4207374051 1 udp 1685790463 191.228.238.68 99999999 typ srflx raddr 192.168.0.278 rport 53991 generation 0 network-id 3", true},
		{nil, "4207374051 1 udp 1685790463 191.228.238.68 53991 typ srflx raddr", true},
		{nil, "4207374051 1 udp 1685790463 191.228.238.68 53991 typ srflx raddr 192.168.0.278 rport 99999999 generation 0 network-id 3", true},
		{nil, "4207374051 INVALID udp 2130706431 10.0.75.1 53634 typ host", true},
		{nil, "4207374051 1 udp INVALID 10.0.75.1 53634 typ host", true},
		{nil, "4207374051 INVALID udp 2130706431 10.0.75.1 INVALID typ host", true},
		{nil, "4207374051 1 udp 2130706431 10.0.75.1 53634 typ INVALID", true},
		{nil, "4207374051 1 INVALID 2130706431 10.0.75.1 53634 typ host", true},
	} {
		t.Run(strconv.Itoa(idx), func(t *testing.T) {
			actualCandidate, err := UnmarshalCandidate(test.marshaled)
			if test.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			require.True(t, test.candidate.Equal(actualCandidate))
			require.Equal(t, test.marshaled, actualCandidate.Marshal())
		})
	}
}

func TestCandidateWriteTo(t *testing.T) {
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP:   net.IP{127, 0, 0, 1},
		Port: 0,
	})
	require.NoError(t, err, "error creating test TCP listener")

	conn, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
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
