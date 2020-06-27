package ice

import (
	"regexp"
	"sync"
	"testing"
)

func TestMathRandomGenerator(t *testing.T) {
	g := newMathRandomGenerator()
	isLetter := regexp.MustCompile(`^[a-zA-Z]+$`).MatchString

	for i := 0; i < 10000; i++ {
		s := g.GenerateString(10, runesAlpha)
		if len(s) != 10 {
			t.Error("Generator returned invalid length")
		}
		if !isLetter(s) {
			t.Errorf("Generator returned unexpected character: %s", s)
		}
	}
}

func TestCryptoRandomGenerator(t *testing.T) {
	isLetter := regexp.MustCompile(`^[a-zA-Z]+$`).MatchString

	for i := 0; i < 10000; i++ {
		s, err := generateCryptoRandomString(10, runesAlpha)
		if err != nil {
			t.Error(err)
		}
		if len(s) != 10 {
			t.Error("Generator returned invalid length")
		}
		if !isLetter(s) {
			t.Errorf("Generator returned unexpected character: %s", s)
		}
	}
}

func TestRandomGeneratorCollision(t *testing.T) {
	candidateIDGen := newCandidateIDGenerator()

	testCases := map[string]struct {
		gen func(t *testing.T) string
	}{
		"CandidateID": {
			gen: func(t *testing.T) string {
				return candidateIDGen.Generate()
			},
		},
		"PWD": {
			gen: func(t *testing.T) string {
				s, err := generatePwd()
				if err != nil {
					t.Fatal(err)
				}
				return s
			},
		},
		"Ufrag": {
			gen: func(t *testing.T) string {
				s, err := generateUFrag()
				if err != nil {
					t.Fatal(err)
				}
				return s
			},
		},
	}

	const N = 1000
	const iteration = 100

	for name, testCase := range testCases {
		testCase := testCase
		t.Run(name, func(t *testing.T) {
			for iter := 0; iter < iteration; iter++ {
				var wg sync.WaitGroup
				var mu sync.Mutex

				rands := make([]string, 0, N)

				for i := 0; i < N; i++ {
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

				if len(rands) != N {
					t.Fatal("Failed to generate randoms")
				}

				for i := 0; i < N; i++ {
					for j := i + 1; j < N; j++ {
						if rands[i] == rands[j] {
							t.Fatalf("generateRandString caused collision: %s == %s", rands[i], rands[j])
						}
					}
				}
			}
		})
	}
}
