// Pure helpers: reading the player's stream and packing bars into Raster cells.

import type { FmStatus } from '../types'

export type Line =
  | { kind: 'hello'; version: number }
  | { kind: 'status'; status: FmStatus }
  | { kind: 'bars'; bars: Uint8Array }
  | { kind: 'ping' }

// One line of the stream (PROTOCOL.md), or null when it isn't one we know.
export function parseLine(line: string): Line | null {
  if (line.startsWith('B ')) {
    const hex = line.slice(2)
    if (hex.length % 2 !== 0) return null
    const bars = new Uint8Array(hex.length / 2)
    for (let i = 0; i < bars.length; i++) {
      const v = parseInt(hex.slice(2 * i, 2 * i + 2), 16)
      if (Number.isNaN(v)) return null
      bars[i] = v
    }
    return { kind: 'bars', bars }
  }
  if (line.startsWith('S ')) {
    try {
      return { kind: 'status', status: JSON.parse(line.slice(2)) as FmStatus }
    } catch {
      return null
    }
  }
  if (line.startsWith('cc-fm ')) return { kind: 'hello', version: Number(line.slice(6)) }
  if (line === '.') return { kind: 'ping' }
  return null
}

// Splits streamed text into whole lines, keeping the unfinished tail.
export function splitLines(buffer: string, text: string): { lines: string[]; rest: string } {
  const parts = (buffer + text).split('\n')
  const rest = parts.pop() ?? ''
  return { lines: parts.filter(Boolean), rest }
}

// Fits the player's bands to `columns` bars, each the loudest band it covers.
export function resample(bands: Uint8Array, columns: number): Uint8Array {
  const out = new Uint8Array(columns)
  if (bands.length === 0) return out
  for (let c = 0; c < columns; c++) {
    const from = Math.floor((c * bands.length) / columns)
    const to = Math.max(from + 1, Math.floor(((c + 1) * bands.length) / columns))
    let v = 0
    for (let b = from; b < to; b++) v = Math.max(v, bands[b] ?? 0)
    out[c] = v
  }
  return out
}

const GLYPHS = [0x2581, 0x2582, 0x2583, 0x2584, 0x2585, 0x2586, 0x2587, 0x2588] // ▁ … █
const DEFAULT_COLOR = 0x01000000
type RGB = readonly [number, number, number]

const CORAL: RGB = [0xd9, 0x77, 0x57]
const VIOLET: RGB = [0x9b, 0x87, 0xf5]
const QUIET: RGB = [0x55, 0x55, 0x5f]

function mix(a: RGB, b: RGB, t: number): RGB {
  const lerp = (x: number, y: number) => Math.round(x + (y - x) * t)
  return [lerp(a[0], b[0]), lerp(a[1], b[1]), lerp(a[2], b[2])]
}

function rgb([r, g, b]: RGB): number {
  return (r << 16) | (g << 8) | b
}

// Raster cells for one row of bars: coral to violet left to right, low bars
// fading toward grey so silence reads as a calm flat line.
export function encodeCells(bars: Uint8Array): string {
  const words = new Uint32Array(bars.length * 3)
  for (let i = 0; i < bars.length; i++) {
    const v = (bars[i] ?? 0) / 255
    const hue = mix(CORAL, VIOLET, bars.length > 1 ? i / (bars.length - 1) : 0)
    words[3 * i] = GLYPHS[Math.min(GLYPHS.length - 1, Math.floor(v * GLYPHS.length))] ?? 0x2581
    words[3 * i + 1] = rgb(mix(QUIET, hue, Math.min(1, 0.35 + v)))
    words[3 * i + 2] = DEFAULT_COLOR
  }
  return toBase64(new Uint8Array(words.buffer))
}

const B64 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/'

function toBase64(bytes: Uint8Array): string {
  let out = ''
  for (let i = 0; i < bytes.length; i += 3) {
    const n = ((bytes[i] ?? 0) << 16) | ((bytes[i + 1] ?? 0) << 8) | (bytes[i + 2] ?? 0)
    out += B64.charAt((n >> 18) & 63) + B64.charAt((n >> 12) & 63)
    out += i + 1 < bytes.length ? B64.charAt((n >> 6) & 63) : '='
    out += i + 2 < bytes.length ? B64.charAt(n & 63) : '='
  }
  return out
}
