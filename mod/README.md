# cc-fm-mod

claude.fm in your Claude Code prompt. While the cc-fm player is playing, this mod draws a live spectrum at the right of the hint row under the composer. It also adds `/fm`, to play, stop and change the volume, and a set of keyboard controls above the composer.

![cc-fm in Claude Code: live bars under the prompt, /fm status on the rail, and the /fm keys controls lighting up with ctrl+x tab](https://raw.githubusercontent.com/code-akram/cc-fm-mod/main/docs/demo.gif)

An unofficial community project, not affiliated with or endorsed by Anthropic. Works in the Claude Code terminal (2.1.287 or later).

## It needs the cc-fm player

The mod doesn't play audio itself. The cc-fm player does, on the machine with your speakers. Install it with `brew install code-akram/tap/cc-fm` (or see the [project README](https://github.com/code-akram/cc-fm-mod) for the install script and `go install`), then run `cc-fm serve`. If Claude Code runs on a remote machine, forward the player's socket over SSH; the project README shows how. Until a player answers, the mod stays out of the way and checks again every few seconds.

## Commands

| Command | Does |
|---|---|
| `/fm` | play or stop |
| `/fm vol <0-100>` | set the volume |
| `/fm status` | what's playing |
| `/fm play <url>` | play a stream or a video |
| `/fm keys` | show controls above the composer. `ctrl+x tab` takes them, then `a` play, `s` stop, `h` quieter, `l` louder, `x` close; `esc` returns to the prompt |
| `/fm help` | what `/fm` takes |

## What it runs and sends

The mod runs no programs, and nothing it does leaves your machine. All it does is make HTTP requests to the cc-fm player over the player's local unix socket, `~/.cc-fm/fm.sock` (or the path in `$CC_FM_SOCKET`), through Claude Code's own `$.http` call with its `socketPath` option:

- `GET /v1/frames`, about 15 times a second while the player plays (once a second while it doesn't): the latest bar frame and the player's status
- `GET /v1/status`, and `POST /v1/play`, `/v1/stop` and `/v1/volume`, when you run `/fm` or press a control

It reads two environment variables, `CC_FM_SOCKET` and `HOME`, to find the socket. It reads and writes no files and collects no data. The player, a separate program you install yourself, is what fetches the radio stream.

It also hooks two Claude Code events, `ui.focus` and `ui.press`, only to notice when `ctrl+x tab` moves into its own controls or a key is pressed there, so it can light the rail. It passes both events on unchanged and ignores any that aren't for its own controls.

## License

MIT
