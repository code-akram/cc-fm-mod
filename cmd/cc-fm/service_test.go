package main

import (
	"strings"
	"testing"
)

func TestSystemdUnit(t *testing.T) {
	unit := systemdUnit("/home/a/.local/bin/cc-fm", []string{"--volume", "40", "--yt-dlp-args", "--cookies-from-browser firefox"}, "/usr/bin:/bin")
	for _, want := range []string{
		"ExecStart=/home/a/.local/bin/cc-fm serve --volume 40 --yt-dlp-args \"--cookies-from-browser firefox\"\n",
		"Environment=PATH=/usr/bin:/bin\n",
		"Restart=on-failure\n",
		"WantedBy=default.target\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
}

func TestSystemdQuote(t *testing.T) {
	for in, want := range map[string]string{
		"plain":       "plain",
		"two words":   `"two words"`,
		`a"b`:         `"a\"b"`,
		"cost $5 50%": `"cost $$5 50%%"`,
		"":            `""`,
	} {
		if got := systemdQuote(in); got != want {
			t.Errorf("systemdQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestLaunchdPlist(t *testing.T) {
	plist := launchdPlist("com.github.code-akram.cc-fm", "/Users/a/.local/bin/cc-fm", []string{"--volume", "40"}, "/opt/homebrew/bin:/usr/bin", "/Users/a/Library/Logs/cc-fm.log")
	for _, want := range []string{
		"<string>com.github.code-akram.cc-fm</string>",
		"<string>/Users/a/.local/bin/cc-fm</string>\n    <string>serve</string>\n    <string>--volume</string>\n    <string>40</string>",
		"<key>PATH</key>\n    <string>/opt/homebrew/bin:/usr/bin</string>",
		"<key>KeepAlive</key>\n  <true/>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %q:\n%s", want, plist)
		}
	}
	if strings.Contains(launchdPlist("l", "/a&b", nil, "", ""), "/a&b") {
		t.Error("plist didn't escape &")
	}
}
