package ice

import "testing"

func TestCandidatePairPriority(t *testing.T) {
	for _, test := range []struct {
		CandidatePair *candidatePair
		WantPriority  uint64
	}{
		{
			CandidatePair: &candidatePair{
				iceRoleControlling: true,
				local: &Candidate{
					Type:            CandidateTypeHost,
					LocalPreference: 65535,
					Component:       ComponentRTP,
				},
				remote: &Candidate{
					Type:            CandidateTypeRelay,
					LocalPreference: 0,
					Component:       ComponentRTP,
				},
			},
			WantPriority: 1099478073343,
		},
		{
			CandidatePair: &candidatePair{
				iceRoleControlling: false,
				local: &Candidate{
					Type:            CandidateTypeHost,
					LocalPreference: 65535,
					Component:       ComponentRTP,
				},
				remote: &Candidate{
					Type:            CandidateTypeRelay,
					LocalPreference: 0,
					Component:       ComponentRTP,
				},
			},
			WantPriority: 1099478073342,
		},
	} {
		if got, want := test.CandidatePair.Priority(), test.WantPriority; got != want {
			t.Fatalf("candidatePair(%v).Priority() = %d, want %d", test.CandidatePair, got, want)
		}
	}
}
