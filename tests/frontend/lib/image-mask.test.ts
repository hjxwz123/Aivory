import { describe, expect, it, vi } from 'vitest'
import { drawMaskStroke, maskPoint, selectionToMask, validMaskDimensions } from '@/lib/image-mask'

describe('OpenAI image masks', () => {
  it('makes painted pixels transparent and unselected pixels opaque', () => {
    const pixels = new Uint8ClampedArray([0, 0, 0, 0, 8, 145, 178, 255, 8, 145, 178, 1])
    expect(selectionToMask(pixels)).toBe(true)
    expect([...pixels]).toEqual([0, 0, 0, 255, 0, 0, 0, 0, 0, 0, 0, 0])
  })
  it('rejects an empty or fully erased selection', () => {
    expect(selectionToMask(new Uint8ClampedArray(16))).toBe(false)
  })
  it('maps CSS coordinates into original image pixels without resizing', () => {
    const rect = { left: 10, top: 20, width: 400, height: 200 }
    expect(maskPoint(210, 120, rect, 1600, 800)).toEqual({ x: 800, y: 400 })
    expect(maskPoint(-100, 999, rect, 1600, 800)).toEqual({ x: 0, y: 800 })
  })
  it('bounds canvas allocation', () => {
    expect(validMaskDimensions(4096, 4096)).toBe(true)
    expect(validMaskDimensions(8192, 8192)).toBe(false)
    expect(validMaskDimensions(0, 1024)).toBe(false)
    expect(validMaskDimensions(8193, 64)).toBe(false)
  })
  it('uses destination-out for erasing and retains the same stroke geometry', () => {
    const context = {
      save: vi.fn(), restore: vi.fn(), beginPath: vi.fn(), arc: vi.fn(), fill: vi.fn(), moveTo: vi.fn(), lineTo: vi.fn(), stroke: vi.fn(),
      globalCompositeOperation: '', lineWidth: 0,
    }
    drawMaskStroke(context as unknown as CanvasRenderingContext2D, { points: [{ x: 20, y: 30 }, { x: 40, y: 50 }], radius: 12, erase: true })
    expect(context.globalCompositeOperation).toBe('destination-out')
    expect(context.lineWidth).toBe(24)
    expect(context.moveTo).toHaveBeenCalledWith(20, 30)
    expect(context.lineTo).toHaveBeenCalledWith(40, 50)
  })
})
