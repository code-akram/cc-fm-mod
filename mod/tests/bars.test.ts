import { expect, test } from 'claude-code/testing'

import { encodeCells, parseLine, resample, splitLines } from '../hooks/bars'

test('parses bar frames, status and keepalives', async () => {
  const bars = parseLine('B 00ff7f')
  expect(bars?.kind).toBe('bars')
  expect(bars?.kind === 'bars' && Array.from(bars.bars)).toEqual([0, 255, 127])

  const status = parseLine('S {"state":"playing","volume":70}')
  expect(status?.kind === 'status' && status.status.state).toBe('playing')

  expect(parseLine('.')?.kind).toBe('ping')
  expect(parseLine('cc-fm 1')).toEqual({ kind: 'hello', version: 1 })
  expect(parseLine('B 0')).toBe(null)
  expect(parseLine('S {broken')).toBe(null)
  expect(parseLine('who knows')).toBe(null)
})

test('keeps a line split across two pieces', async () => {
  const first = splitLines('', 'B 00ff\nB 0')
  expect(first.lines).toEqual(['B 00ff'])
  const second = splitLines(first.rest, '1ff\n')
  expect(second.lines).toEqual(['B 01ff'])
  expect(second.rest).toBe('')
})

test('resamples bands to columns, keeping each span’s loudest', async () => {
  const bands = new Uint8Array([10, 200, 30, 40, 50, 60, 70, 255])
  expect(Array.from(resample(bands, 4))).toEqual([200, 40, 60, 255])
  expect(resample(bands, 24).length).toBe(24)
  expect(Array.from(resample(new Uint8Array(0), 3))).toEqual([0, 0, 0])
})

test('packs one u32 triplet per cell', async () => {
  const cells = encodeCells(new Uint8Array([0, 128, 255]))
  // 3 cells × 3 words × 4 bytes = 36 bytes = 48 base64 characters, no padding.
  expect(cells.length).toBe(48)
  expect(cells.endsWith('=')).toBe(false)
})
