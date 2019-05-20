package ice

import (
	"testing"

	"github.com/gortc/stun"
)

func TestUseCandidateAttr_AddTo(t *testing.T) {
	m := new(stun.Message)
	if UseCandidate.IsSet(m) {
		t.Error("should not be set")
	}
	if err := m.Build(stun.BindingRequest, UseCandidate); err != nil {
		t.Error(err)
	}
	m1 := new(stun.Message)
	if _, err := m1.Write(m.Raw); err != nil {
		t.Error(err)
	}
	if !UseCandidate.IsSet(m1) {
		t.Error("should be set")
	}
}
