//go:build !js
// +build !js

package ice

import (
	"testing"
	"time"

	"github.com/pion/transport/test"
	"github.com/stretchr/testify/assert"
)

func TestPairSearch(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 10)
	defer lim.Stop()

	var config AgentConfig
	a, err := NewAgent(&config)
	if err != nil {
		t.Fatalf("Error constructing ice.Agent")
	}

	if len(a.checklist) != 0 {
		t.Fatalf("TestPairSearch is only a valid test if a.validPairs is empty on construction")
	}

	cp := a.getBestAvailableCandidatePair()

	if cp != nil {
		t.Fatalf("No Candidate pairs should exist")
	}

	assert.NoError(t, a.Close())
}
