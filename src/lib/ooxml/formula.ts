/**
 * Safe formula evaluation for the spreadsheet editor.
 *
 * Deliberately NOT `new Function` / `eval`: formula text comes from a file the
 * user opened, and evaluating it as JavaScript would run attacker-controlled
 * code in this origin. Untrusted markup elsewhere in the app is confined to an
 * opaque-origin sandbox; a spreadsheet formula has no such boundary, so this
 * module uses an explicit tokenizer + Pratt parser over a fixed grammar, with a
 * whitelist of functions backed by `@formulajs/formulajs`.
 *
 * Anything outside the grammar or the whitelist raises `FormulaUnsupported`,
 * which callers surface honestly instead of showing a wrong number.
 */
import {
  ABS,
  AND,
  AVERAGE,
  CONCATENATE,
  COUNT,
  COUNTA,
  IF,
  IFERROR,
  INT,
  LEN,
  MAX,
  MIN,
  MOD,
  NOT,
  OR,
  POWER,
  ROUND,
  SQRT,
  SUM,
  TRIM,
  UPPER,
} from '@formulajs/formulajs'

export class FormulaUnsupported extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'FormulaUnsupported'
  }
}

export type FormulaValue = number | string | boolean | null

export interface FormulaContext {
  /** Cached value of a single cell, or null when empty/unknown. */
  valueOf(ref: string): FormulaValue
  /** Cached values across an inclusive rectangular range, row-major. */
  rangeOf(start: string, end: string): FormulaValue[]
}

/** Whitelisted functions. Everything else is reported as unsupported. */
const FUNCTIONS: Record<string, (...args: never[]) => FormulaValue> = {
  ABS, AND, AVERAGE, CONCATENATE, COUNT, COUNTA, IF, IFERROR, INT, LEN, MAX, MIN,
  MOD, NOT, OR, POWER, ROUND, SQRT, SUM, TRIM, UPPER,
} as unknown as Record<string, (...args: never[]) => FormulaValue>

type Token =
  | { kind: 'number'; value: number }
  | { kind: 'string'; value: string }
  | { kind: 'ref'; value: string }
  | { kind: 'range'; start: string; end: string }
  | { kind: 'name'; value: string }
  | { kind: 'op'; value: string }

const REF = /^[A-Za-z]{1,3}\d{1,7}$/

function tokenize(input: string): Token[] {
  const tokens: Token[] = []
  let index = 0

  while (index < input.length) {
    const char = input[index]!

    if (/\s/.test(char)) {
      index += 1
      continue
    }

    if (/[0-9.]/.test(char)) {
      const match = /^\d*\.?\d+(?:[eE][+-]?\d+)?/.exec(input.slice(index))
      if (!match) throw new FormulaUnsupported(`Bad number at ${index}`)
      tokens.push({ kind: 'number', value: Number(match[0]) })
      index += match[0].length
      continue
    }

    if (char === '"') {
      const end = input.indexOf('"', index + 1)
      if (end < 0) throw new FormulaUnsupported('Unterminated string')
      tokens.push({ kind: 'string', value: input.slice(index + 1, end) })
      index = end + 1
      continue
    }

    if (/[A-Za-z_]/.test(char)) {
      const match = /^[A-Za-z_][A-Za-z0-9_.]*/.exec(input.slice(index))!
      let end = index + match[0].length
      // A cell reference or range, as opposed to a function name.
      if (REF.test(match[0])) {
        const rangeMatch = /^:([A-Za-z]{1,3}\d{1,7})/.exec(input.slice(end))
        if (rangeMatch && REF.test(rangeMatch[1]!)) {
          tokens.push({ kind: 'range', start: match[0].toUpperCase(), end: rangeMatch[1]!.toUpperCase() })
          end += rangeMatch[0].length
        } else {
          tokens.push({ kind: 'ref', value: match[0].toUpperCase() })
        }
      } else {
        tokens.push({ kind: 'name', value: match[0].toUpperCase() })
      }
      index = end
      continue
    }

    const two = input.slice(index, index + 2)
    if (two === '<=' || two === '>=' || two === '<>') {
      tokens.push({ kind: 'op', value: two })
      index += 2
      continue
    }

    if ('+-*/^&=<>(),'.includes(char)) {
      tokens.push({ kind: 'op', value: char })
      index += 1
      continue
    }

    throw new FormulaUnsupported(`Unexpected character "${char}"`)
  }

  return tokens
}

const PRECEDENCE: Record<string, number> = {
  '=': 1, '<>': 1, '<': 1, '>': 1, '<=': 1, '>=': 1,
  '&': 2, '+': 3, '-': 3, '*': 4, '/': 4, '^': 5,
}

function scalar(value: FormulaValue): number | string {
  if (value === null || typeof value === 'boolean') return value === true ? 1 : value === false ? 0 : 0
  return value
}

function numeric(value: FormulaValue): number {
  const raw = scalar(value)
  if (typeof raw === 'number') return raw
  const parsed = Number(raw)
  if (Number.isNaN(parsed)) throw new FormulaUnsupported('Non-numeric operand')
  return parsed
}

function isTruthy(value: FormulaValue): boolean {
  if (value === null) return false
  if (typeof value === 'boolean') return value
  if (typeof value === 'number') return value !== 0
  return value.trim() !== ''
}

export function evaluateFormula(formula: string, context: FormulaContext): FormulaValue {
  const tokens = tokenize(formula)
  let position = 0

  const peek = () => tokens[position]
  const eat = () => tokens[position++]

  function expectOp(value: string): void {
    const token = peek()
    if (!token || token.kind !== 'op' || token.value !== value) {
      throw new FormulaUnsupported(`Expected "${value}"`)
    }
    position += 1
  }

  /** A bare range is only valid as a direct function argument. */
  function asScalar(value: FormulaValue | FormulaValue[]): FormulaValue {
    if (Array.isArray(value)) {
      throw new FormulaUnsupported('A range must be used inside a function')
    }
    return value
  }

  function parseArg(): FormulaValue[] {
    const token = peek()
    if (token?.kind === 'range') {
      position += 1
      return context.rangeOf(token.start, token.end)
    }
    return [parseExpression(0)]
  }

  function parseArgs(): FormulaValue[] {
    expectOp('(')
    const args: FormulaValue[] = []
    if (peek()?.kind === 'op' && (peek() as { value: string }).value === ')') {
      position += 1
      return args
    }
    for (;;) {
      args.push(...parseArg())
      const token = peek()
      if (token?.kind === 'op' && token.value === ',') {
        position += 1
        continue
      }
      expectOp(')')
      return args
    }
  }

  function parsePrimary(): FormulaValue | FormulaValue[] {
    const token = eat()
    if (!token) throw new FormulaUnsupported('Unexpected end of formula')

    if (token.kind === 'number') return token.value
    if (token.kind === 'string') return token.value
    if (token.kind === 'ref') return context.valueOf(token.value)
    if (token.kind === 'range') return context.rangeOf(token.start, token.end)
    if (token.kind === 'op') {
      if (token.value === '(') {
        const inner = parseExpression(0)
        expectOp(')')
        return inner
      }
      if (token.value === '-') return -numeric(asScalar(parsePrimary()))
      if (token.value === '+') return numeric(asScalar(parsePrimary()))
      throw new FormulaUnsupported(`Unexpected operator "${token.value}"`)
    }

    // Function call.
    const fn = FUNCTIONS[token.value]
    if (!fn) throw new FormulaUnsupported(`Unsupported function ${token.value}`)
    const args = parseArgs()
    return fn(...(args as never[]))
  }

  function parseExpression(minPrecedence: number): FormulaValue {
    let left = asScalar(parsePrimary())

    for (;;) {
      const token = peek()
      if (!token || token.kind !== 'op') break
      const precedence = PRECEDENCE[token.value]
      if (precedence === undefined || precedence < minPrecedence) break
      position += 1
      const right = asScalar(parseExpression(precedence + 1))

      switch (token.value) {
        case '+': left = numeric(left) + numeric(right); break
        case '-': left = numeric(left) - numeric(right); break
        case '*': left = numeric(left) * numeric(right); break
        case '/': {
          const divisor = numeric(right)
          if (divisor === 0) throw new FormulaUnsupported('Division by zero')
          left = numeric(left) / divisor
          break
        }
        case '^': left = numeric(left) ** numeric(right); break
        case '&': left = `${scalar(left)}${scalar(right)}`; break
        case '=': left = scalar(left) === scalar(right); break
        case '<>': left = scalar(left) !== scalar(right); break
        case '<': left = numeric(left) < numeric(right); break
        case '>': left = numeric(left) > numeric(right); break
        case '<=': left = numeric(left) <= numeric(right); break
        case '>=': left = numeric(left) >= numeric(right); break
        default: throw new FormulaUnsupported(`Unsupported operator ${token.value}`)
      }
    }

    return left
  }

  const result = parseExpression(0)
  if (position !== tokens.length) throw new FormulaUnsupported('Trailing input in formula')
  if (typeof result === 'boolean') return result
  if (typeof result === 'number' && !Number.isFinite(result)) throw new FormulaUnsupported('Non-finite result')
  return result
}

/** Display text for an evaluated value, matching how the grid shows cells. */
export function formulaValueToText(value: FormulaValue): string {
  if (value === null) return ''
  if (typeof value === 'boolean') return value ? 'TRUE' : 'FALSE'
  if (typeof value === 'number') return String(Math.round(value * 1e10) / 1e10)
  return value
}

/** Formulajs throws on genuinely bad input; callers degrade instead of crashing. */
export function evaluateFormulaSafely(formula: string, context: FormulaContext): string | Error {
  try {
    return formulaValueToText(evaluateFormula(formula, context))
  } catch (error) {
    return error instanceof Error ? error : new Error(String(error))
  }
}
