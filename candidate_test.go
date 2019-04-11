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
				LocalPreference: 65535,
				Component:       ComponentRTP,
			},
			WantPriority: 2130706431,
		},
		{
			Candidate: &Candidate{
				Type:            CandidateTypeRelay,
				LocalPreference: 0,
				Component:       ComponentRTP,
			},
			WantPriority: 255,
		},
	} {
		if got, want := test.Candidate.Priority(), test.WantPriority; got != want {
			t.Fatalf("Candidate(%v).Priority() = %d, want %d", test.Candidate, got, want)
		}
	}
}
