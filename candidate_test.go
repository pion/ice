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
