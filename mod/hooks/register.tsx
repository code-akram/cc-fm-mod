import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { FmStatus } from '../types'
import { encodeCells, parseLine, resample, splitLines } from './bars'
import { RED, VIOLET, keyHint, listening, meter, railed, stateItem } from './views'

const status = atom({ plugin: 'cc-fm-mod', key: 'status' } as const, { state: 'offline' } as FmStatus)
// Whether the controls above the composer are showing (/fm keys).
const isKeysOpen = atom({ plugin: 'cc-fm-mod', key: 'isKeysOpen' } as const, false)
// Whether the person is driving the controls: lit from the moment ctrl+x tab
// focuses them, dimmed again once they type or go quiet (Claude Code says
// when the band takes the keyboard, not when it gives it back).
const isControlling = atom({ plugin: 'cc-fm-mod', key: 'isControlling' } as const, false)

const COLUMNS = 24
// Below this width the hint row keeps all its room for the engine's own line.
const MIN_VIEWPORT = 60
const RETRY_MS = 3000
// Repaints cost the terminal real CPU at 30 fps, so a session paints every
// 2nd frame (15 fps) while busy and every 4th (~8 fps) while idle, and skips
// frames that look the same as the last one at the bars' resolution.
const BUSY_EVERY = 2
const IDLE_EVERY = 4
// Frames between checks that the session's state still holds the status.
const RESYNC_EVERY = 60

// Volume change per press of j or k in the controls.
const VOLUME_STEP = 5
// Rows the controls need with connectors between items, and without; with
// fewer they take one line.
const RAIL_ROWS = 11
const TIGHT_RAIL_ROWS = 6
// How long the controls stay lit with no key pressed in them.
const CONTROL_IDLE_MS = 8000

// What /fm takes, for its typeahead line and its usage reply.
const COMMANDS: [string, string][] = [
  ['/fm', 'play or stop'],
  ['/fm vol <0-100>', 'set the volume'],
  ['/fm status', 'what’s playing'],
  ['/fm play <url>', 'play a stream or a video'],
  ['/fm keys', 'controls you drive from the keyboard'],
]

// What a /fm reply showed, so its transcript row can draw it on the rail.
// Keyed by the reply's text, which is also what the model reads.
type Reply =
  | { kind: 'status'; st: FmStatus }
  | { kind: 'volume'; st: FmStatus }
  | { kind: 'usage' }
  | { kind: 'offline'; socket: string }
  | { kind: 'error'; message: string }
const replies = new Map<string, Reply>()

class Offline extends Error {}

let socket: string | undefined
// The hint row's requestId while it shows our bars, for blitting into.
let site: string | undefined
let isBusy = false
let frames = 0
let bars: Uint8Array = new Uint8Array(COLUMNS)
let lastStatus = ''
// The player's latest status, kept to restore after a /clear resets state.
let known: FmStatus = { state: 'offline' }
let lastCells = ''

// The player's socket: $CC_FM_SOCKET, or ~/.cc-fm/fm.sock.
async function socketPath($: EngineInterface): Promise<string> {
  if (socket) return socket
  const { stdout } = await $.process.run([
    'sh',
    '-c',
    'p=${CC_FM_SOCKET:-$HOME/.cc-fm/fm.sock}; case $p in "~/"*) p=$HOME/${p#"~/"};; esac; printf %s "$p"',
  ])
  socket = stdout
  return socket
}

async function setStatus($: EngineInterface, next: FmStatus) {
  known = next
  const json = JSON.stringify(next)
  if (json === lastStatus) return
  lastStatus = json
  await update($, status, () => next)
}

let lastControl = 0

// Lights the controls and keeps them lit while keys keep coming.
async function controlling($: EngineInterface) {
  lastControl = Date.now()
  await update($, isControlling, () => true)
  void $.clock.after(CONTROL_IDLE_MS + 100, () => settle($))
}

// Dims the controls once no key has been pressed in them for a while.
async function settle($: EngineInterface) {
  if (Date.now() - lastControl >= CONTROL_IDLE_MS) await update($, isControlling, () => false)
}

async function release($: EngineInterface) {
  lastControl = 0
  await update($, isControlling, () => false)
}

// A /clear starts a new session whose state starts over, and no event says
// so; the player only reports changes. While bars flow, check now and then.
async function resync($: EngineInterface) {
  if (JSON.stringify(await read($, status)) !== lastStatus) {
    await update($, status, () => known)
  }
}

function paint($: EngineInterface, frame: Uint8Array) {
  frames++
  if (frames % RESYNC_EVERY === 0) void resync($)
  if (!site || frames % (isBusy ? BUSY_EVERY : IDLE_EVERY) !== 0) return
  bars = resample(frame, COLUMNS)
  const cells = encodeCells(bars)
  if (cells === lastCells) return
  lastCells = cells
  void $.ui.blit({ requestId: site, key: 'bars', cells })
}

// Holds the stream open for the session's life, reconnecting while no
// player answers. A hot reload ends the loop with the old module.
async function listen($: EngineInterface) {
  const path = await socketPath($)
  for (;;) {
    let buffer = ''
    try {
      const stream = $.process.spawn({ argv: ['curl', '-sN', '--unix-socket', path, 'http://cc-fm/v1/stream'] })
      for await (const piece of stream) {
        if (piece.stream !== 'stdout') continue
        const split = splitLines(buffer, piece.text)
        buffer = split.rest
        for (const line of split.lines) {
          const msg = parseLine(line)
          if (msg?.kind === 'status') await setStatus($, msg.status)
          else if (msg?.kind === 'bars') paint($, msg.bars)
        }
      }
    } catch {
      // curl missing or refused to start: same as no player, try again later.
    }
    await setStatus($, { state: 'offline' })
    await $.clock.sleep(RETRY_MS)
  }
}

async function api($: EngineInterface, method: 'GET' | 'POST', path: string, body?: object): Promise<FmStatus> {
  const argv = ['curl', '-sS', '--max-time', '5', '--unix-socket', await socketPath($), '-X', method]
  if (body) argv.push('-H', 'Content-Type: application/json', '--data', JSON.stringify(body))
  argv.push(`http://cc-fm${path}`)
  const { exitCode, stdout } = await $.process.run(argv, { timeoutMs: 8000 })
  if (exitCode !== 0) throw new Offline()
  try {
    return JSON.parse(stdout) as FmStatus
  } catch {
    throw new Error(stdout.trim() || 'the player gave no answer')
  }
}

// A control-row press: send it, and show the player's answer at once.
async function control($: EngineInterface, method: 'GET' | 'POST', path: string, body?: object) {
  try {
    await setStatus($, await api($, method, path, body))
  } catch (err) {
    $.ui.toast(err instanceof Offline ? 'No cc-fm player answers.' : `cc-fm: ${String(err)}`)
  }
}

// The marker for control and command items on the rail.
const VIOLET_ITEM = VIOLET

// Whether a /fm argument is one it knows, rather than a typo for usage.
function isCommandWord(word: string, verb: string): boolean {
  return (
    ['stop', 'off', 'play', 'on', 'vol', 'volume', 'status', 'keys', 'help'].includes(word) ||
    /^\d+$/.test(word) ||
    verb.includes('://')
  )
}

// Records how a reply draws and returns it, its text a plain version.
function reply(r: Reply): { text: string } {
  const text = plainText(r)
  replies.set(text, r)
  return { text }
}

function plainText(r: Reply): string {
  switch (r.kind) {
    case 'status':
      return [`claude.fm: ${r.st.state}`, `vol ${r.st.volume ?? 0}`, listening(r.st), r.st.title, r.st.error]
        .filter(Boolean)
        .join(' · ')
    case 'volume':
      return `claude.fm: volume ${r.st.volume ?? 0}`
    case 'usage':
      return COMMANDS.map(([cmd, what]) => `${cmd}: ${what}`).join(' · ')
    case 'offline':
      return `claude.fm: no cc-fm player answers on ${r.socket}. Start one where your speakers are: cc-fm serve`
    case 'error':
      return `claude.fm: ${r.message}`
  }
}

export const register: Register = on => {
  socket = undefined

  on('session.start', async ($, e, next) => {
    const started = await next(e)
    await $.command.register({
      name: 'fm',
      description: 'claude.fm in your prompt: play, stop, volume and controls',
      argumentHint: '[stop | vol <0-100> | status | keys | play <url> | help]',
      immediate: true,
    })
    void listen($)
    return started
  })

  on('command.run', { command: 'fm' }, async ($, e) => {
    const [verb = '', ...rest] = e.args.trim().split(/\s+/).filter(Boolean)
    const word = verb.toLowerCase()
    try {
      if (word === 'keys') {
        await release($)
        await update($, isKeysOpen, isOpen => !isOpen)
        return {}
      }
      if (word === 'help') return reply({ kind: 'usage' })
      if (word !== '' && !isCommandWord(word, verb)) {
        return reply({ kind: 'error', message: `/fm doesn’t take “${verb}”` })
      }

      let st: FmStatus
      if (word === '') {
        const now = await api($, 'GET', '/v1/status')
        const isOn = now.state === 'playing' || now.state === 'connecting' || now.state === 'retrying'
        st = await api($, 'POST', isOn ? '/v1/stop' : '/v1/play')
      } else if (word === 'stop' || word === 'off') {
        st = await api($, 'POST', '/v1/stop')
      } else if (word === 'play' || word === 'on') {
        st = await api($, 'POST', '/v1/play', { source: rest.join(' ') })
      } else if (verb.includes('://')) {
        st = await api($, 'POST', '/v1/play', { source: [verb, ...rest].join(' ') })
      } else if (word === 'vol' || word === 'volume' || /^\d+$/.test(word)) {
        const volume = Number(/^\d+$/.test(word) ? word : rest[0])
        if (!Number.isInteger(volume) || volume < 0 || volume > 100) {
          return reply({ kind: 'error', message: 'volume is 0 to 100, as in /fm vol 40' })
        }
        st = await api($, 'POST', '/v1/volume', { volume })
        await setStatus($, st)
        return reply({ kind: 'volume', st })
      } else {
        st = await api($, 'GET', '/v1/status')
      }
      await setStatus($, st)
      return reply({ kind: 'status', st })
    } catch (err) {
      if (err instanceof Offline) return reply({ kind: 'offline', socket: await socketPath($) })
      return reply({ kind: 'error', message: err instanceof Error ? err.message : String(err) })
    }
  })

  // Every /fm reply draws on the rail, in place of the plain row.
  on('ui.render', { component: 'CommandOutput' }, async ($, e, next) => {
    // The row's text arrives under the plugin's name: "cc-fm-mod: …".
    const text = e.props.text.replace(/^cc-fm-mod: /, '')
    const r = e.props.command === 'fm' && e.surface === 'terminal' ? replies.get(text) : undefined
    if (!r || e.surface !== 'terminal') return next(e)
    const ui = $.ui.resolve(e)
    const { Text } = ui
    switch (r.kind) {
      case 'status':
        return railed(
          ui,
          [
            stateItem(ui, r.st, `vol ${r.st.volume ?? 0}`),
            ...(r.st.error ? [{ glyph: '✕', color: RED, content: <Text dimColor>{r.st.error}</Text> }] : []),
          ],
          <Text dimColor>{[r.st.title, listening(r.st)].filter(Boolean).join('  ·  ') || 'claude.fm'}</Text>,
        )
      case 'volume':
        return railed(
          ui,
          [],
          <Text>
            <Text>{`volume ${String(r.st.volume ?? 0).padStart(3)}  `}</Text>
            {meter(ui, r.st.volume ?? 0)}
          </Text>,
        )
      case 'usage':
        return railed(
          ui,
          COMMANDS.slice(0, -1).map(([cmd, what]) => ({
            glyph: '◇',
            color: VIOLET_ITEM,
            content: keyHint(ui, cmd.padEnd(16), what),
          })),
          keyHint(ui, COMMANDS[COMMANDS.length - 1]![0].padEnd(16), COMMANDS[COMMANDS.length - 1]![1]),
        )
      case 'offline':
        return railed(
          ui,
          [{ glyph: '○', content: <Text dimColor>{`no player answers on ${r.socket}`}</Text> }],
          keyHint(ui, 'cc-fm serve', 'start one where your speakers are; over SSH, forward the socket'),
        )
      case 'error':
        return railed(ui, [{ glyph: '✕', color: RED, content: <Text>{r.message}</Text> }], keyHint(ui, '/fm help', 'what /fm takes'))
    }
  })

  // Focus entering the controls (ctrl+x tab lands on play) or a key pressed
  // in them lights the rail.
  on('ui.focus', async ($, e, next) => {
    if (e.component === 'AbovePrompt' && e.plugin === 'cc-fm-mod' && e.element) await controlling($)
    return next(e)
  })
  on('ui.press', async ($, e, next) => {
    if (e.component === 'AbovePrompt' && e.plugin === 'cc-fm-mod') await controlling($)
    return next(e)
  })

  // The controls above the composer, opened by /fm keys: ctrl+x tab takes
  // them, then vim-style keys (a play, s stop, h quieter, l louder, x close)
  // and esc back to the prompt. Lit while driven, receded at rest, the
  // leader named once, on its own line.
  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    if (e.surface !== 'terminal' || e.props.hasSurvey || !(await read($, isKeysOpen))) return next(e)
    const ui = $.ui.resolve(e)
    const { Box, Button, Text } = ui
    const st = await read($, status)
    const isLit = await read($, isControlling)
    const volume = st.volume ?? 0
    const isPlaying = st.state === 'playing'
    const isStopped = st.state === 'stopped' || st.state === 'offline'

    const play = (
      <Button key="play" hotkey="a" plain autoFocus label="play" dimColor={!isLit || isPlaying} onPress={() => control($, 'POST', '/v1/play')} />
    )
    const stop = <Button key="stop" hotkey="s" plain label="stop" dimColor={!isLit || isStopped} onPress={() => control($, 'POST', '/v1/stop')} />
    const quieter = (
      <Button
        key="down"
        hotkey="h"
        plain
        label="quieter"
        dimColor={!isLit || volume === 0}
        onPress={() => control($, 'POST', '/v1/volume', { volume: Math.max(0, volume - VOLUME_STEP) })}
      />
    )
    const louder = (
      <Button
        key="up"
        hotkey="l"
        plain
        label="louder"
        dimColor={!isLit || volume === 100}
        onPress={() => control($, 'POST', '/v1/volume', { volume: Math.min(100, volume + VOLUME_STEP) })}
      />
    )
    const close = (
      <Button
        key="close"
        hotkey="x"
        plain
        label="close"
        dimColor={!isLit}
        onPress={async () => {
          await release($)
          await update($, isKeysOpen, () => false)
        }}
      />
    )
    const leader = isLit ? keyHint(ui, 'esc', 'back to the prompt') : keyHint(ui, 'ctrl+x tab', 'take the controls')

    if (e.props.maxRows < TIGHT_RAIL_ROWS) {
      return (
        <Box flexDirection="row" gap={2}>
          <Text bold>♪ claude.fm</Text>
          {stateItem(ui, st, undefined, !isLit).content}
          {play}
          {stop}
          {quieter}
          <Text dimColor={!isLit}>{volume}</Text>
          {louder}
          {close}
          {leader}
        </Box>
      )
    }

    const hasConnectors = e.props.maxRows >= RAIL_ROWS
    return railed(
      ui,
      [
        stateItem(ui, st, undefined, !isLit),
        {
          glyph: '◇',
          color: VIOLET_ITEM,
          content: (
            <Box flexDirection="row" gap={4}>
              {play}
              {stop}
            </Box>
          ),
        },
        {
          glyph: '◇',
          color: VIOLET_ITEM,
          content: (
            <Box flexDirection="row" gap={2}>
              {quieter}
              {meter(ui, volume, !isLit)}
              <Text dimColor={!isLit}>{String(volume).padStart(3)}</Text>
              {louder}
            </Box>
          ),
        },
        { glyph: '◇', color: VIOLET_ITEM, content: close },
      ],
      leader,
      isLit ? 'active' : 'receded',
      e.props.maxRows > RAIL_ROWS,
      hasConnectors,
    )
  })

  on('ui.render', { component: 'PromptHint' }, async ($, e, next) => {
    // Typing in the prompt, or a turn, means the controls gave the keys back.
    if ((e.props.isDraft || e.props.isWorking) && lastControl > 0) void $.clock.after(0, () => release($))
    if (e.surface !== 'terminal') return next(e)
    const st = await read($, status)
    const isShown =
      st.state !== 'offline' && st.state !== 'stopped' && (e.viewport?.columns ?? MIN_VIEWPORT) >= MIN_VIEWPORT
    if (!isShown) {
      site = undefined
      return next(e)
    }

    site = e.requestId
    isBusy = e.props.isWorking || e.props.isDraft
    const { Box, Raster, Text } = $.ui.resolve(e)
    const engine = await next(e)
    const label =
      st.state === 'playing' ? '♪ ' : st.state === 'connecting' ? 'fm · tuning… ' : 'fm · reconnecting… '

    // The engine's own line can't sit under a Box with a width, so the row
    // grows into the hint area instead and a spacer pushes the bars right.
    return (
      <Box flexDirection="row" flexGrow={1}>
        {engine}
        <Box flexGrow={1} />
        <Box flexDirection="row" flexShrink={0}>
          <Text dimColor>{label}</Text>
          {st.state === 'playing' && <Raster key="bars" columns={COLUMNS} rows={1} cells={encodeCells(bars)} />}
        </Box>
      </Box>
    )
  })
}
