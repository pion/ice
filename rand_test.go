// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRandomGeneratorCollision(t *testing.T) {
	candidateIDGen := newCandidateIDGenerator()

	testCases := map[string]struct {
		gen func(t *testing.T) string
	}{
		"CandidateID": {
			gen: func(*testing.T) string {
				return candidateIDGen.Generate()
			},
		},
		"PWD": {
			gen: func(t *testing.T) string {
				t.Helper()

				s, err := generatePwd()
				require.NoError(t, err)

				return s
			},
		},
		"Ufrag": {
			gen: func(t *testing.T) string {
				t.Helper()

				s, err := generateUFrag()
				require.NoError(t, err)

				return s
			},
		},
	}

	const num = 100
	const iteration = 100

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			for range iteration {
				var wg sync.WaitGroup
				var mu sync.Mutex

				rands := make([]string, 0, num)

				for range num {
					wg.Add(1)
					go func() {
						r := testCase.gen(t)
						mu.Lock()
						rands = append(rands, r)
						mu.Unlock()
						wg.Done()
					}()
				}
				wg.Wait()

				require.Len(t, rands, num)
				for i := range num {
					for j := i + 1; j < num; j++ {
						require.NotEqual(t, rands[i], rands[j])
					}
				}
			}
		})
	}
}
