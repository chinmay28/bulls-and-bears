import { describe, expect, it } from 'vitest'
import { parts, pct, usd } from './format'

describe('usd', () => {
  it('prints a statement figure', () => {
    expect(usd(10342.18)).toBe('$10,342.18')
    expect(usd(41.2, true)).toBe('+$41.20')
    expect(usd(-312.55)).toBe('−$312.55')
    expect(usd(0, true)).toBe('$0.00')
  })
})

describe('pct', () => {
  it('turns a fraction into a percentage with a real minus sign', () => {
    expect(pct(0.021)).toBe('2.1%')
    expect(pct(-0.087)).toBe('−8.7%')
    expect(pct(0.034, true)).toBe('+3.4%')
  })
})

describe('parts', () => {
  it('drops the pieces with nothing to say', () => {
    expect(parts('a', false, null, undefined, '', 'b')).toBe('a · b')
  })
})
