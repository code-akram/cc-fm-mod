# cc-fm protocol, version 1

The player (`cc-fm serve`) speaks HTTP on a unix socket, `~/.cc-fm/fm.sock` by default (owner-only, `0600`). Over SSH the socket is forwarded with `RemoteForward`, so a remote session talks to the player on your own machine exactly as a local one does. Only bars and status cross the wire. Audio never does.

## Commands

| Request | Body | Effect |
|---|---|---|
| `GET /v1/status` | none | current status |
| `POST /v1/play` | `{"source": "..."}`, optional | play `source`, or `https://clau.de/radio` when empty |
| `POST /v1/stop` | none | stop |
| `POST /v1/volume` | `{"volume": 0-100}` | set the volume, live |

Each command answers with the status object. `POST /v1/play` refuses (`403`) any source but `demo`, an empty one or an `http(s)` URL. Requests may arrive from another machine through the SSH forward, and local paths and ffmpeg graphs would reach this machine's files.

## Status

```json
{"state":"playing","source":"https://clau.de/radio","title":"…","volume":70,
 "output":"audiotoolbox","error":"","version":"0.1.0","bands":32,"fps":30,"listeners":2}
```

`state` is one of `stopped`, `connecting`, `playing` or `retrying`. While retrying, `error` says why. `output` is the audio device; `null` means the player's machine has no speakers and serves bars only.

## Stream: `GET /v1/stream`

Newline-delimited text, one message per line:

| Line | Meaning |
|---|---|
| `cc-fm 1` | first line: protocol version |
| `S <json>` | status, sent on connect and on every change (listeners joining and leaving included) |
| `B <hex>` | one frame of bars, two hex digits (0–255) per band, low to high frequency, `fps` times a second while playing |
| `.` | keepalive, every 5 s while not playing |

A listener that falls behind loses frames rather than delaying the others. Unknown lines are ignored, so later versions can add message types.
