package storage

import "testing"

func TestPickLetter(t *testing.T) {
	if got := PickLetter(nil); got != "A" {
		t.Fatalf("first letter %q", got)
	}
	if got := PickLetter([]string{"A", "b", "C"}); got != "D" {
		t.Fatalf("after A B C: %q", got)
	}
	all := make([]string, 0, 26)
	for i := 0; i < 26; i++ {
		all = append(all, Letters[i:i+1])
	}
	if got := PickLetter(all); got != "" {
		t.Fatalf("exhausted alphabet gave %q", got)
	}
}

func TestNextName(t *testing.T) {
	if got := NextName("a", nil); got != "Apollo" {
		t.Fatalf("first A name %q", got)
	}
	if got := NextName("A", []string{"apollo", "Atlas"}); got != "Aurora" {
		t.Fatalf("third A name %q", got)
	}
	used := append([]string{}, driveNames['A']...)
	if got := NextName("A", used); got != "A-drive-1" {
		t.Fatalf("overflow name %q", got)
	}
	if got := NextName("", nil); got != "" {
		t.Fatalf("no letter gave %q", got)
	}
}
