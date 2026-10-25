package main

import "testing"

func TestHostLabel(t *testing.T) {
	for in, want := range map[string]string{"Test1": "test1", "Living Room NAS": "living-room-nas", "  Café ": "caf", "a--b": "a--b"} {
		if got := hostLabel(in); got != want {
			t.Errorf("hostLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
