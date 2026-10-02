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

Everything the mod does stays on your machine. It talks only to the cc-fm player's local unix socket, `~/.cc-fm/fm.sock`, or the path in `$CC_FM_SOCKET`. Specifically it runs:

- `curl --unix-socket <socket> http://cc-fm/...`, to read the bars and the player's status and to send play, stop and volume requests
- `sh -c` once per load, to work out the socket path from `$CC_FM_SOCKET` and `$HOME`

It reads and writes no files, makes no network requests, and collects no data. The player, a separate program you install yourself, is what fetches the radio stream.

## License

MIT
