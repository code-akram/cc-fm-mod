package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := statePath("")
	if got := loadState(path); got.Volume != nil {
		t.Fatalf("fresh state has volume %d, want none", *got.Volume)
	}
	v := 33
	if err := saveState(path, state{Volume: &v}); err != nil {
		t.Fatal(err)
	}
	if got := loadState(path); got.Volume == nil || *got.Volume != 33 {
		t.Fatalf("loaded %v, want 33", got.Volume)
	}
}

func TestStateIgnoresJunk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".cc-fm", "state.json")
	for _, junk := range []string{"not json", `{"volume": 400}`, `{"volume": -1}`} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(junk), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := loadState(path); got.Volume != nil {
			t.Fatalf("%q loaded volume %d, want none", junk, *got.Volume)
		}
	}
}

func TestStatePathPerSocket(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	main := filepath.Join(home, ".cc-fm", "state.json")
	if got := statePath(filepath.Join(home, ".cc-fm", "fm.sock")); got != main {
		t.Fatalf("default socket keeps %s, want %s", got, main)
	}
	if got := statePath(""); got != main {
		t.Fatalf("no socket keeps %s, want %s", got, main)
	}
	if got := statePath("/tmp/test.sock"); got != "/tmp/test.sock.state.json" {
		t.Fatalf("other socket keeps %s, want it beside the socket", got)
	}
}
