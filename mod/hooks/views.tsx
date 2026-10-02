// The mod's one visual language, after the clack-style CLI steppers: a thin
// left rail (┌ │ └) holding one item a line, state carried by a coloured
// glyph, the text plain and calm. The controls pane and every /fm reply
// draw with it, so the command reads as one piece.

import type { Elements, RenderElement, RenderNode } from 'claude-code'

import type { FmStatus } from '../types'

type Ui = Pick<Elements['terminal'], 'Box' | 'Text'>

// The visualizer's own colours, so the rail and the bars belong together.
export const CORAL = '#d97757'
export const VIOLET = '#9b87f5'
export const AMBER = '#e0af68'
export const RED = '#e06c75'

export const METER_CELLS = 16

// One item on the rail: its glyph, and what it says.
export type Item = { glyph: string; color?: string; content: RenderNode }

// How a railed block reads: static for replies in the transcript; active for
// the controls while they have the keyboard, the rail lit in the
// visualizer's coral; receded for the controls without it, all dimmed.
export type Mode = 'static' | 'active' | 'receded'

// A railed block: the title, items with a connector between each (unless
// hasConnectors is false, for tight spaces), and a closing line, after a
// blank line unless isSpaced is false.
export function railed(
  ui: Ui,
  items: Item[],
  footer: RenderNode,
  mode: Mode = 'static',
  isSpaced = true,
  hasConnectors = true,
): RenderElement {
  const { Box, Text } = ui
  const isDim = mode === 'receded'
  const railColor = mode === 'active' ? CORAL : undefined
  const edge = (glyph: string) => (
    <Text color={railColor} dimColor={!railColor}>
      {glyph}
    </Text>
  )
  const rows: RenderNode[] = [
    <Box flexDirection="row" gap={2}>
      {edge('┌')}
      <Text bold={!isDim} dimColor={isDim}>
        ♪ claude.fm
      </Text>
    </Box>,
  ]
  for (const item of items) {
    if (hasConnectors) rows.push(edge('│'))
    rows.push(
      <Box flexDirection="row" gap={2}>
        <Text color={isDim ? undefined : item.color} dimColor={isDim || !item.color}>
          {item.glyph}
        </Text>
        {item.content}
      </Box>,
    )
  }
  if (items.length > 0 && hasConnectors) rows.push(edge('│'))
  rows.push(
    <Box flexDirection="row" gap={2}>
      {edge('└')}
      {footer}
    </Box>,
  )
  return (
    <Box flexDirection="column" marginTop={isSpaced ? 1 : 0}>
      {rows}
    </Box>
  )
}

// The player's state as a rail item: coral ● playing, ○ stopped, amber ◌
// on its way.
export function stateItem(ui: Ui, st: FmStatus, detail?: string, isDim = false): Item {
  const { Text } = ui
  const label =
    st.state === 'playing'
      ? 'playing'
      : st.state === 'stopped'
        ? 'stopped'
        : st.state === 'connecting'
          ? 'connecting…'
          : st.state === 'retrying'
            ? 'reconnecting…'
            : 'offline'
  const glyph = st.state === 'playing' ? '●' : st.state === 'stopped' || st.state === 'offline' ? '○' : '◌'
  const color = st.state === 'playing' ? CORAL : st.state === 'stopped' || st.state === 'offline' ? undefined : AMBER
  return {
    glyph,
    color,
    content: (
      <Text>
        <Text dimColor={isDim || st.state !== 'playing'}>{label}</Text>
        {detail ? <Text dimColor>{`  ·  ${detail}`}</Text> : null}
      </Text>
    ),
  }
}

export function meter(ui: Ui, volume: number, isDim = false): RenderNode {
  const { Text } = ui
  const filled = Math.round((Math.min(100, Math.max(0, volume)) / 100) * METER_CELLS)
  return (
    <Text>
      <Text color={isDim ? undefined : CORAL} dimColor={isDim}>
        {'━'.repeat(filled)}
      </Text>
      <Text dimColor>{'─'.repeat(METER_CELLS - filled)}</Text>
    </Text>
  )
}

// A key and what it does, for footers: the key bold, the words dim.
export function keyHint(ui: Ui, key: string, words: string): RenderNode {
  const { Text } = ui
  return (
    <Text>
      <Text bold>{key}</Text>
      <Text dimColor>{`  ${words}`}</Text>
    </Text>
  )
}

export function listening(st: FmStatus): string | undefined {
  if (!st.listeners) return undefined
  return `${st.listeners} session${st.listeners === 1 ? '' : 's'} listening`
}
