package ice

import (
	"regexp"
	"testing"
)

func TestRandSeq(t *testing.T) {
	if len(randSeq(10)) != 10 {
		t.Errorf("randSeq return invalid length")
	}

	var isLetter = regexp.MustCompile(`^[a-zA-Z]+$`).MatchString
	if !isLetter(randSeq(10)) {
		t.Errorf("randSeq should be AlphaNumeric only")
	}
}
