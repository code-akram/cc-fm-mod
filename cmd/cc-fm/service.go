package main

import (
	"errors"
	"flag"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// service keeps the player running at login: a launchd agent on macOS, a
// systemd user unit on Linux. Homebrew installs have `brew services` for
// that, so a Homebrew binary is pointed there instead.
//
//	cc-fm service install [--name N] [--dry-run] [-- serve flags…]
//	cc-fm service uninstall [--name N] [--dry-run]
//	cc-fm service status [--name N]
func service(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cc-fm service install|uninstall|status [--name N] [--dry-run] [-- serve flags…]")
	}
	verb, args := args[0], args[1:]

	// Flags after "--" go to `cc-fm serve`, as in `-- --volume 40`.
	var serveArgs []string
	for i, a := range args {
		if a == "--" {
			serveArgs, args = args[i+1:], args[:i]
			break
		}
	}
	fs := flag.NewFlagSet("service "+verb, flag.ExitOnError)
	name := fs.String("name", "cc-fm", "service name, to run more than one")
	dryRun := fs.Bool("dry-run", false, "show what would be written and run, and change nothing")
	force := fs.Bool("force", false, "install even though this cc-fm came from Homebrew")
	_ = fs.Parse(args)

	svc, err := newService(*name)
	if err != nil {
		return err
	}
	switch verb {
	case "install":
		if !*force && isHomebrew() {
			return errors.New("this cc-fm came from Homebrew: run `brew services start cc-fm` instead (or pass --force)")
		}
		return svc.install(serveArgs, *dryRun)
	case "uninstall":
		return svc.uninstall(*dryRun)
	case "status":
		return svc.status()
	}
	return fmt.Errorf("unknown service command %q: install, uninstall or status", verb)
}

type serviceDef struct {
	name, exe, file, log, label string
	isLaunchd                   bool
}

func newService(name string) (serviceDef, error) {
	if name == "" || strings.ContainsAny(name, "/ \t\n") {
		return serviceDef{}, fmt.Errorf("bad service name %q", name)
	}
	exe, err := os.Executable()
	if err != nil {
		return serviceDef{}, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return serviceDef{}, err
	}
	switch runtime.GOOS {
	case "darwin":
		label := "com.github.code-akram." + name
		return serviceDef{
			name: name, exe: exe, label: label, isLaunchd: true,
			file: filepath.Join(home, "Library", "LaunchAgents", label+".plist"),
			log:  filepath.Join(home, "Library", "Logs", name+".log"),
		}, nil
	case "linux":
		config := os.Getenv("XDG_CONFIG_HOME")
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		return serviceDef{
			name: name, exe: exe, label: name + ".service",
			file: filepath.Join(config, "systemd", "user", name+".service"),
		}, nil
	}
	return serviceDef{}, fmt.Errorf("cc-fm service supports macOS and Linux, not %s", runtime.GOOS)
}

func (s serviceDef) install(serveArgs []string, dryRun bool) error {
	path := servicePath()
	var body string
	var steps [][]string
	if s.isLaunchd {
		body = launchdPlist(s.label, s.exe, serveArgs, path, s.log)
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		steps = [][]string{
			{"launchctl", "bootout", domain + "/" + s.label},
			{"launchctl", "bootstrap", domain, s.file},
		}
	} else {
		body = systemdUnit(s.exe, serveArgs, path)
		steps = [][]string{
			{"systemctl", "--user", "daemon-reload"},
			{"systemctl", "--user", "enable", "--now", s.label},
		}
	}

	if dryRun {
		fmt.Printf("would write %s:\n\n%s\nwould run:\n", s.file, body)
		for _, step := range steps {
			fmt.Println("  " + strings.Join(step, " "))
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.file, []byte(body), 0o644); err != nil {
		return err
	}
	for i, step := range steps {
		out, err := exec.Command(step[0], step[1:]...).CombinedOutput()
		// Booting out an agent that isn't loaded yet fails, and that's fine.
		if err != nil && !(s.isLaunchd && i == 0) {
			return fmt.Errorf("%s: %v\n%s", strings.Join(step, " "), err, strings.TrimSpace(string(out)))
		}
	}
	fmt.Printf("installed %s: cc-fm serve runs now and at each login\n  %s\n", s.label, s.file)
	if s.log != "" {
		fmt.Printf("  log: %s\n", s.log)
	} else {
		fmt.Printf("  log: journalctl --user -u %s\n", s.label)
	}
	return nil
}

func (s serviceDef) uninstall(dryRun bool) error {
	var steps [][]string
	if s.isLaunchd {
		steps = [][]string{{"launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), s.label)}}
	} else {
		steps = [][]string{{"systemctl", "--user", "disable", "--now", s.label}}
	}
	if dryRun {
		fmt.Println("would run:")
		for _, step := range steps {
			fmt.Println("  " + strings.Join(step, " "))
		}
		fmt.Printf("would remove %s\n", s.file)
		return nil
	}
	if _, err := os.Stat(s.file); err != nil {
		return fmt.Errorf("%s is not installed (%s missing)", s.label, s.file)
	}
	for _, step := range steps {
		_ = exec.Command(step[0], step[1:]...).Run()
	}
	if err := os.Remove(s.file); err != nil {
		return err
	}
	if !s.isLaunchd {
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	}
	fmt.Printf("uninstalled %s\n", s.label)
	return nil
}

func (s serviceDef) status() error {
	if _, err := os.Stat(s.file); err != nil {
		fmt.Printf("%s: not installed\n", s.label)
		return nil
	}
	var check *exec.Cmd
	if s.isLaunchd {
		check = exec.Command("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), s.label))
	} else {
		check = exec.Command("systemctl", "--user", "is-active", "--quiet", s.label)
	}
	state := "installed, not running"
	if check.Run() == nil {
		state = "installed and running"
	}
	fmt.Printf("%s: %s\n  %s\n", s.label, state, s.file)
	return nil
}

// servicePath is the PATH a service runs with: wherever ffmpeg and yt-dlp
// are now, then the usual places. launchd and systemd don't inherit the
// login shell's PATH.
func servicePath() string {
	var dirs []string
	add := func(d string) {
		for _, have := range dirs {
			if have == d {
				return
			}
		}
		dirs = append(dirs, d)
	}
	for _, tool := range []string{"ffmpeg", "yt-dlp"} {
		if p, err := exec.LookPath(tool); err == nil {
			add(filepath.Dir(p))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".cc-fm", "bin"))
	}
	for _, d := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"} {
		add(d)
	}
	return strings.Join(dirs, ":")
}

func isHomebrew() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return strings.Contains(exe, "/Cellar/")
}

func launchdPlist(label, exe string, serveArgs []string, path, log string) string {
	x := html.EscapeString
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + x(label) + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + x(exe) + `</string>
    <string>serve</string>
`)
	for _, a := range serveArgs {
		b.WriteString("    <string>" + x(a) + "</string>\n")
	}
	b.WriteString(`  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>` + x(path) + `</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>` + x(log) + `</string>
  <key>StandardErrorPath</key>
  <string>` + x(log) + `</string>
</dict>
</plist>
`)
	return b.String()
}

func systemdUnit(exe string, serveArgs []string, path string) string {
	cmd := []string{systemdQuote(exe), "serve"}
	for _, a := range serveArgs {
		cmd = append(cmd, systemdQuote(a))
	}
	return `[Unit]
Description=cc-fm player: claude.fm for Claude Code
After=network-online.target sound.target

[Service]
ExecStart=` + strings.Join(cmd, " ") + `
Environment=` + systemdQuote("PATH="+path) + `
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`
}

// systemdQuote quotes a word for a unit file when it needs it.
func systemdQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'\\$%;") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`, `%`, `%%`)
	return `"` + r.Replace(s) + `"`
}
