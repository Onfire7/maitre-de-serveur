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
	if got := NextName("A", used); got != "Apollo 2" {
		t.Fatalf("overflow name %q", got)
	}
	used = append(used, "Apollo 2")
	if got := NextName("A", used); got != "Atlas 2" {
		t.Fatalf("second overflow name %q", got)
	}
	for l := byte('A'); l <= 'Z'; l++ {
		if len(driveNames[l]) != 29 {
			t.Fatalf("letter %c has %d names, want 29", l, len(driveNames[l]))
		}
		for _, n := range driveNames[l] {
			if n[0] != l {
				t.Fatalf("%q listed under %c", n, l)
			}
		}
	}
	if got := NextName("", nil); got != "" {
		t.Fatalf("no letter gave %q", got)
	}
}
