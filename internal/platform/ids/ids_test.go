package ids

import (
	"regexp"
	"testing"
)

func TestNew(t *testing.T) {
	re := regexp.MustCompile(`^cmp_[0-9a-f]{12}$`)
	a, b := New("cmp"), New("cmp")
	if !re.MatchString(a) {
		t.Fatalf("id %q does not match %s", a, re)
	}
	if a == b {
		t.Fatalf("two ids are equal: %q", a)
	}
}
