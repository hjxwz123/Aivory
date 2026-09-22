import { describe, expect, it } from 'vitest'
import {
  FormulaUnsupported,
  evaluateFormula,
  formulaValueToText,
  type FormulaContext,
} from '@/lib/ooxml/formula'

const CELLS: Record<string, number | string> = {
  A1: 10,
  A2: 20,
  A3: 30,
  B1: 2,
  B2: 'text',
  C1: 5,
  D1: 7,
  D2: 9,
}

const context: FormulaContext = {
  valueOf: (ref) => CELLS[ref] ?? null,
  rangeOf: (start, end) => {
    const startColumn = start.charCodeAt(0)
    const startRow = Number(start.slice(1))
    const endColumn = end.charCodeAt(0)
    const endRow = Number(end.slice(1))
    const values: Array<number | string | null> = []
    for (let row = startRow; row <= endRow; row += 1) {
      for (let column = startColumn; column <= endColumn; column += 1) {
        values.push(CELLS[`${String.fromCharCode(column)}${row}`] ?? null)
      }
    }
    return values
  },
}

const evaluate = (formula: string) => formulaValueToText(evaluateFormula(formula, context))

describe('evaluateFormula', () => {
  it('evaluates arithmetic with correct precedence', () => {
    expect(evaluate('1+2')).toBe('3')
    expect(evaluate('1+2*3')).toBe('7')
    expect(evaluate('(1+2)*3')).toBe('9')
    expect(evaluate('2^3')).toBe('8')
    expect(evaluate('-4+1')).toBe('-3')
    expect(evaluate('10/4')).toBe('2.5')
  })

  it('resolves cell references and ranges', () => {
    expect(evaluate('A1*B1')).toBe('20')
    expect(evaluate('A1+A2+A3')).toBe('60')
    expect(evaluate('SUM(A1:A3)')).toBe('60')
    expect(evaluate('AVERAGE(A1:A3)')).toBe('20')
    expect(evaluate('MAX(A1:A3)')).toBe('30')
    expect(evaluate('MIN(A1:A3)')).toBe('10')
    expect(evaluate('COUNT(A1:A3)')).toBe('3')
  })

  it('supports the whitelisted scalar functions', () => {
    expect(evaluate('ABS(-3)')).toBe('3')
    expect(evaluate('ROUND(1.2345,2)')).toBe('1.23')
    expect(evaluate('INT(2.9)')).toBe('2')
    expect(evaluate('SQRT(16)')).toBe('4')
    expect(evaluate('MOD(7,3)')).toBe('1')
    expect(evaluate('POWER(2,5)')).toBe('32')
    expect(evaluate('LEN("hello")')).toBe('5')
    expect(evaluate('UPPER("abc")')).toBe('ABC')
  })

  it('handles comparisons and IF', () => {
    expect(evaluate('IF(A1>5,"big","small")')).toBe('big')
    expect(evaluate('IF(A1>50,"big","small")')).toBe('small')
    expect(evaluate('A1>A2')).toBe('FALSE')
    expect(evaluate('AND(A1>5,B1>1)')).toBe('TRUE')
    expect(evaluate('OR(A1>50,B1>1)')).toBe('TRUE')
    expect(evaluate('NOT(A1>5)')).toBe('FALSE')
  })

  it('concatenates with & and CONCATENATE', () => {
    expect(evaluate('"a"&"b"')).toBe('ab')
    expect(evaluate('A1&"-"&B1')).toBe('10-2')
    expect(evaluate('CONCATENATE("x",1)')).toBe('x1')
  })

  it('reads empty cells as zero, like a spreadsheet', () => {
    expect(evaluate('Z99+1')).toBe('1')
    expect(evaluate('SUM(Z1:Z3)')).toBe('0')
  })

  it('reports unsupported functions and syntax instead of guessing', () => {
    expect(() => evaluateFormula('VLOOKUP(A1,A1:B2,1)', context)).toThrow(FormulaUnsupported)
    expect(() => evaluateFormula('A1+', context)).toThrow(FormulaUnsupported)
    expect(() => evaluateFormula('SUM(A1:A3', context)).toThrow(FormulaUnsupported)
    expect(() => evaluateFormula('1/0', context)).toThrow(FormulaUnsupported)
  })

  /**
   * The reason this module does not use `new Function`: a formula is attacker
   * controlled when it comes from an opened file. Every one of these must raise
   * rather than execute.
   */
  it('cannot be used to execute JavaScript', () => {
    const payloads = [
      '1;globalThis.__pwned=1',
      'constructor.constructor("return 1")()',
      'globalThis.__pwned=1',
      'A1.__proto__',
      '(()=>1)()',
      'process.exit(1)',
      'eval("1")',
    ]
    for (const payload of payloads) {
      expect(() => evaluateFormula(payload, context)).toThrow(FormulaUnsupported)
    }
    expect((globalThis as Record<string, unknown>).__pwned).toBeUndefined()
  })
})
