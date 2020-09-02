package ice

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

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
			WantPriority: 2128609279,
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
			WantPriority: 2124414975,
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
			WantPriority: 2120220671,
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
			WantPriority: 1860173823,
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
			WantPriority: 1855979519,
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
			WantPriority: 1851785215,
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
	assert.Equal(t, candidate.LastSent(), time.Time{})
	now := time.Now()
	candidate.setLastSent(now)
	assert.Equal(t, candidate.LastSent(), now)
}

func TestCandidateLastReceived(t *testing.T) {
	candidate := candidateBase{}
	assert.Equal(t, candidate.LastReceived(), time.Time{})
	now := time.Now()
	candidate.setLastReceived(now)
	assert.Equal(t, candidate.LastReceived(), now)
}

func TestCandidateMarshal(t *testing.T) {
	for _, test := range []struct {
		candidate   Candidate
		marshaled   string
		expectError bool
	}{
		{&CandidateHost{
			candidateBase{
				networkType:   NetworkTypeUDP6,
				candidateType: CandidateTypeHost,
				address:       "fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a",
				port:          53987,
			},
			"",
		},
			"1938809241 1 udp 2130706431 fcd9:e3b8:12ce:9fc5:74a5:c6bb:d8b:e08a 53987 typ host",
			false,
		},
		{&CandidateHost{
			candidateBase{
				networkType:   NetworkTypeUDP4,
				candidateType: CandidateTypeHost,
				address:       "10.0.75.1",
				port:          53634,
			},
			"",
		},
			"1986380506 1 udp 2130706431 10.0.75.1 53634 typ host",
			false,
		},
		{&CandidateServerReflexive{
			candidateBase{
				networkType:    NetworkTypeUDP4,
				candidateType:  CandidateTypeServerReflexive,
				address:        "191.228.238.68",
				port:           53991,
				relatedAddress: &CandidateRelatedAddress{"192.168.0.274", 53991},
			},
		},
			"4207374051 1 udp 1694498815 191.228.238.68 53991 typ srflx raddr 192.168.0.274 rport 53991",
			false,
		},
		{&CandidateRelay{
			candidateBase{
				networkType:    NetworkTypeUDP4,
				candidateType:  CandidateTypeRelay,
				address:        "50.0.0.1",
				port:           5000,
				relatedAddress: &CandidateRelatedAddress{"192.168.0.1", 5001},
			},
			nil,
		},
			"4207374051 1 udp 16777215 50.0.0.1 5000 typ relay raddr 192.168.0.1 rport 5001",
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
	} {
		actualCandidate, err := UnmarshalCandidate(test.marshaled)
		if test.expectError {
			assert.Error(t, err)
			return
		}

		assert.NoError(t, err)
		assert.True(t, test.candidate.Equal(actualCandidate))
		assert.Equal(t, test.marshaled, actualCandidate.Marshal())
	}
}
