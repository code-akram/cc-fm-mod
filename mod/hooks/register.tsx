import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { FmStatus } from '../types'
import { encodeCells, parseLine, resample, splitLines } from './bars'

const status = atom({ plugin: 'cc-fm-mod', key: 'status' } as const, { state: 'offline' } as FmStatus)
// Whether the keyboard control row above the prompt is showing (/fm keys).
const isKeysOpen = atom({ plugin: 'cc-fm-mod', key: 'isKeysOpen' } as const, false)

const COLUMNS = 24
// Below this width the hint row keeps all its room for the engine's own line.
const MIN_VIEWPORT = 72
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
// Rows the controls need for their spaced, three-line layout.
const SPACIOUS_ROWS = 5
// Cells in the controls' volume meter.
const METER_CELLS = 16
const CORAL = '#d97757'

const USAGE = '/fm toggles · /fm stop · /fm vol 40 · /fm status · /fm keys · /fm play <url>'

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

function describe(st: FmStatus): string {
  const parts = [`♪ claude.fm · ${st.state}`]
  if (st.volume !== undefined) parts.push(`vol ${st.volume}`)
  if (st.listeners) parts.push(`${st.listeners} session${st.listeners === 1 ? '' : 's'} listening`)
  if (st.output === 'null') parts.push('no speakers on the player’s machine, bars only')
  let text = parts.join(' · ')
  if (st.title) text += `\n  ${st.title}`
  if (st.error) text += `\n  ${st.error}`
  return text
}

async function offlineHelp($: EngineInterface): Promise<string> {
  const path = await socketPath($)
  return [
    `No cc-fm player answers on ${path}.`,
    'Start one on the machine with your speakers: cc-fm serve',
    `Working over SSH? Forward it in ~/.ssh/config: RemoteForward ${path} <your ~>/.cc-fm/fm.sock`,
  ].join('\n')
}

export const register: Register = on => {
  socket = undefined

  on('session.start', async ($, e, next) => {
    const started = await next(e)
    await $.command.register({
      name: 'fm',
      description: `claude.fm: ${USAGE}`,
      argumentHint: '[stop | vol <0-100> | status | play <url>]',
      immediate: true,
    })
    void listen($)
    return started
  })

  on('command.run', { command: 'fm' }, async ($, e) => {
    const [verb = '', ...rest] = e.args.trim().split(/\s+/).filter(Boolean)
    const word = verb.toLowerCase()
    try {
      let st: FmStatus
      if (word === '') {
        const now = await api($, 'GET', '/v1/status')
        const isOn = now.state === 'playing' || now.state === 'connecting' || now.state === 'retrying'
        st = await api($, 'POST', isOn ? '/v1/stop' : '/v1/play')
      } else if (word === 'stop' || word === 'off') {
        st = await api($, 'POST', '/v1/stop')
      } else if (word === 'play' || word === 'on') {
        st = await api($, 'POST', '/v1/play', { source: rest.join(' ') })
      } else if (verb.includes('://') || verb.startsWith('lavfi:')) {
        st = await api($, 'POST', '/v1/play', { source: [verb, ...rest].join(' ') })
      } else if (word === 'vol' || word === 'volume' || /^\d+$/.test(word)) {
        const volume = Number(/^\d+$/.test(word) ? word : rest[0])
        if (!Number.isInteger(volume) || volume < 0 || volume > 100) return { text: 'Volume is 0–100: /fm vol 40' }
        st = await api($, 'POST', '/v1/volume', { volume })
      } else if (word === 'keys') {
        const isOpen = !(await read($, isKeysOpen))
        await update($, isKeysOpen, () => isOpen)
        return { text: isOpen ? 'Controls open above the prompt · ctrl+x tab to use them' : 'Controls closed' }
      } else if (word === 'status') {
        st = await api($, 'GET', '/v1/status')
      } else {
        return { text: USAGE }
      }
      await setStatus($, st)
      return { text: describe(st) }
    } catch (err) {
      if (err instanceof Offline) return { text: await offlineHelp($) }
      return { text: `cc-fm: ${err instanceof Error ? err.message : String(err)}` }
    }
  })

  // The controls above the prompt, shown by /fm keys. Three lines with room
  // between them where the band has it (state; transport and volume; how to
  // use them), one line where it doesn't. Hotkeys work once ctrl+x tab
  // focuses the band, and every button clicks too.
  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    const st = await read($, status)
    if (!(await read($, isKeysOpen)) || e.props.hasSurvey || st.state === 'offline') {
      return next(e)
    }
    const { Box, Button, Text } = $.ui.resolve(e)
    const volume = st.volume ?? 0
    const isPlaying = st.state === 'playing'
    const isStopped = st.state === 'stopped'
    const stateLabel =
      st.state === 'playing' ? 'playing' : st.state === 'stopped' ? 'stopped' : st.state === 'connecting' ? 'connecting…' : 'reconnecting…'

    const play = <Button key="play" hotkey="p" plain label="play" dimColor={isPlaying} onPress={() => control($, 'POST', '/v1/play')} />
    const stop = <Button key="stop" hotkey="s" plain label="stop" dimColor={isStopped} onPress={() => control($, 'POST', '/v1/stop')} />
    const quieter = (
      <Button
        key="down"
        hotkey="j"
        plain
        label="quieter"
        dimColor={volume === 0}
        onPress={() => control($, 'POST', '/v1/volume', { volume: Math.max(0, volume - VOLUME_STEP) })}
      />
    )
    const louder = (
      <Button
        key="up"
        hotkey="k"
        plain
        label="louder"
        dimColor={volume === 100}
        onPress={() => control($, 'POST', '/v1/volume', { volume: Math.min(100, volume + VOLUME_STEP) })}
      />
    )
    const close = <Button key="close" hotkey="x" plain label="close" onPress={() => update($, isKeysOpen, () => false)} />
    const state = (
      <Text color={isPlaying ? CORAL : undefined} dimColor={!isPlaying}>
        ● {stateLabel}
      </Text>
    )

    // Not enough room for three spaced lines: one line, same keys.
    if (e.props.maxRows < SPACIOUS_ROWS) {
      return (
        <Box flexDirection="row" gap={2}>
          <Text bold>♪ claude.fm</Text>
          {state}
          {play}
          {stop}
          {quieter}
          <Text>{volume}</Text>
          {louder}
          {close}
        </Box>
      )
    }

    const filled = Math.round((volume / 100) * METER_CELLS)
    return (
      <Box flexDirection="column" paddingX={1}>
        <Box flexDirection="row" gap={3}>
          <Text bold>♪ claude.fm</Text>
          {state}
        </Box>
        <Box flexDirection="row" gap={6} marginTop={1}>
          <Box flexDirection="row" gap={3}>
            {play}
            {stop}
          </Box>
          <Box flexDirection="row" gap={2}>
            {quieter}
            <Text>
              <Text color={CORAL}>{'━'.repeat(filled)}</Text>
              <Text dimColor>{'─'.repeat(METER_CELLS - filled)}</Text>
            </Text>
            <Text>{String(volume).padStart(3)}</Text>
            {louder}
          </Box>
        </Box>
        <Box flexDirection="row" gap={6} marginTop={1}>
          <Text>
            <Text bold>ctrl+x tab</Text>
            <Text dimColor>  use these keys</Text>
          </Text>
          <Text>
            <Text bold>esc</Text>
            <Text dimColor>  back to the prompt</Text>
          </Text>
          {close}
        </Box>
      </Box>
    )
  })

  on('ui.render', { component: 'PromptHint' }, async ($, e, next) => {
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
