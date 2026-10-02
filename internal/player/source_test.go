package player

import (
	"context"
	"slices"
	"testing"
)

func TestCheckRemote(t *testing.T) {
	allowed := []string{"", "demo", "https://clau.de/radio", "http://example.com/stream.mp3"}
	refused := []string{
		"/etc/passwd",
		"~/Music/track.ogg",
		"lavfi:sine",
		"lavfi:anullsrc,ametadata=print:file=/tmp/x",
		"file:///etc/passwd",
		"concat:/a|/b",
		"https://",
		"ftp://example.com/a.mp3",
	}
	for _, s := range allowed {
		if err := CheckRemote(s); err != nil {
			t.Errorf("CheckRemote(%q) = %v, want allowed", s, err)
		}
	}
	for _, s := range refused {
		if CheckRemote(s) == nil {
			t.Errorf("CheckRemote(%q) allowed, want refused", s)
		}
	}
}

func TestURLInputsAreNetworkOnly(t *testing.T) {
	in, err := resolve(context.Background(), "https://example.com/stream.mp3", nil)
	if err != nil {
		t.Fatal(err)
	}
	args := in.args
	i := slices.Index(args, "-protocol_whitelist")
	if i < 0 || i+1 >= len(args) || slices.Contains([]string{"file", "concat"}, args[i+1]) {
		t.Fatalf("args = %q, want a network-only protocol whitelist", args)
	}
}
