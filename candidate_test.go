package ice

import "testing"

func TestCandidatePriority(t *testing.T) {
	for _, test := range []struct {
		Candidate    *Candidate
		WantPriority uint32
	}{
		{
			Candidate: &Candidate{
				Type:            CandidateTypeHost,
				LocalPreference: defaultLocalPreference,
				Component:       ComponentRTP,
			},
			WantPriority: 2130706431,
		},
		{
			Candidate: &Candidate{
				Type:            CandidateTypePeerReflexive,
				LocalPreference: defaultLocalPreference,
				Component:       ComponentRTP,
			},
			WantPriority: 1862270975,
		},
		{
			Candidate: &Candidate{
				Type:            CandidateTypeServerReflexive,
				LocalPreference: defaultLocalPreference,
				Component:       ComponentRTP,
			},
			WantPriority: 1694498815,
		},
		{
			Candidate: &Candidate{
				Type:            CandidateTypeRelay,
				LocalPreference: defaultLocalPreference,
				Component:       ComponentRTP,
			},
			WantPriority: 16777215,
		},
	} {
		if got, want := test.Candidate.Priority(), test.WantPriority; got != want {
			t.Fatalf("Candidate(%v).Priority() = %d, want %d", test.Candidate, got, want)
		}
	}
}
