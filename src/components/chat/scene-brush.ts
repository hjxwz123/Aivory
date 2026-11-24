export type Palette = { paper: string; ink: string; lead: string; pale: string; color: string; shade: string; gold: string; white: string }

// Paths use a local sketchbook coordinate system, independent of display size.
export function brush(ctx: CanvasRenderingContext2D, p: Palette) {
  function path(data: string, fill?: string, stroke: string | null = p.ink, width = 1.65) {
    const shape = new Path2D(data)
    if (fill) { ctx.fillStyle = fill; ctx.fill(shape) }
    if (stroke) { ctx.strokeStyle = stroke; ctx.lineWidth = width; ctx.stroke(shape) }
  }
  function oval(x: number, y: number, rx: number, ry: number, fill: string | null, stroke: string | null = p.ink, width = 1.5) {
    ctx.beginPath()
    ctx.ellipse(x, y, rx, ry, 0, 0, Math.PI * 2)
    if (fill) { ctx.fillStyle = fill; ctx.fill() }
    if (stroke) { ctx.strokeStyle = stroke; ctx.lineWidth = width; ctx.stroke() }
  }
  function at(x: number, y: number, scale: number, rotation: number, draw: () => void) {
    ctx.save()
    ctx.translate(x, y)
    ctx.scale(scale, scale)
    ctx.rotate(rotation * Math.PI / 180)
    draw()
    ctx.restore()
  }
  function line(data: string, color = p.ink, width = 1.25) { path(data, undefined, color, width) }
  return { path, oval, at, line, p }
}
export type Brush = ReturnType<typeof brush>

export function seededRandom(seed: number) {
  return () => {
    seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0
    return seed / 4294967296
  }
}
