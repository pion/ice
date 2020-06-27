package ice

import (
	crand "crypto/rand"
	"encoding/binary"
	"math/big"
	mrand "math/rand" // used for non-crypto unique ID and random port selection
	"sync"
	"time"
)

const (
	runesAlpha                 = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	runesDigit                 = "0123456789"
	runesCandidateIDFoundation = runesAlpha + runesDigit + "+/"

	lenUFrag = 16
	lenPwd   = 32
)

// Seeding random generator each time limits number of generated sequence to 31-bits,
// and causes collision on low time accuracy environments.
// Use global random generator seeded by crypto grade random.
var globalMathRandomGenerator = newMathRandomGenerator()
var globalCandidateIDGenerator = candidateIDGenerator{globalMathRandomGenerator}

// mathRandomGenerator is a random generator for non-crypto usage.
type mathRandomGenerator struct {
	r  *mrand.Rand
	mu sync.Mutex
}

func newMathRandomGenerator() *mathRandomGenerator {
	var seed int64
	if err := binary.Read(crand.Reader, binary.LittleEndian, &seed); err != nil {
		// crypto/rand is unavailable. Fallback to seed by time.
		seed = time.Now().UnixNano()
	}

	return &mathRandomGenerator{r: mrand.New(mrand.NewSource(seed))}
}

func (g *mathRandomGenerator) Intn(n int) int {
	g.mu.Lock()
	v := g.r.Intn(n)
	g.mu.Unlock()
	return v
}

func (g *mathRandomGenerator) Uint64() uint64 {
	g.mu.Lock()
	v := g.r.Uint64()
	g.mu.Unlock()
	return v
}

func (g *mathRandomGenerator) GenerateString(n int, runes string) string {
	letters := []rune(runes)
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[g.Intn(len(letters))]
	}
	return string(b)
}

// candidateIDGenerator is a random candidate ID generator.
// Candidate ID is used in SDP and always shared to the other peer.
// It doesn't require cryptographic random.
type candidateIDGenerator struct {
	*mathRandomGenerator
}

func newCandidateIDGenerator() *candidateIDGenerator {
	return &candidateIDGenerator{
		newMathRandomGenerator(),
	}
}

func (g *candidateIDGenerator) Generate() string {
	// https://tools.ietf.org/html/rfc5245#section-15.1
	// candidate-id = "candidate" ":" foundation
	// foundation   = 1*32ice-char
	// ice-char     = ALPHA / DIGIT / "+" / "/"
	return "candidate:" + g.mathRandomGenerator.GenerateString(32, runesCandidateIDFoundation)
}

// generateCryptoRandomString generates a random string for crypto usage.
func generateCryptoRandomString(n int, runes string) (string, error) {
	letters := []rune(runes)
	b := make([]rune, n)
	for i := range b {
		v, err := crand.Int(crand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			return "", err
		}
		b[i] = letters[v.Int64()]
	}
	return string(b), nil
}

// generatePwd generates ICE pwd.
// This internally uses generateCryptoRandomString.
func generatePwd() (string, error) {
	return generateCryptoRandomString(lenPwd, runesAlpha)
}

// generateUFrag generates ICE user fragment.
// This internally uses generateCryptoRandomString.
func generateUFrag() (string, error) {
	return generateCryptoRandomString(lenUFrag, runesAlpha)
}
