/** A single clock owns the entire scene. All coordinates are in the root SVG
 * viewBox; no CSS transforms, path plugin alignment, or inherited origins. */
export const DANDELION_DURATION = 72
export const DANDELION_STILL_TIME = 10

type Point = { x: number; y: number }
type Cubic = [Point, Point, Point, Point]
export interface SeedPose extends Point {
  angle: number
  scale: number
  length: number
  opacity: number
}

const clamp = (value: number) => Math.max(0, Math.min(1, value))
export function easeBetween(time: number, start: number, end: number) {
  const value = clamp((time - start) / (end - start))
  return value * value * (3 - 2 * value)
}
const mix = (a: number, b: number, amount: number) => a + (b - a) * amount
export const sceneOpacity = (time: number) => easeBetween(time, 0, 1.5) * (1 - easeBetween(time, 67, 72))

export function flowerCenter(time: number): Point {
  const breeze = Math.sin(time * .38) * 2.4 + Math.sin(time * .71) * .7
  const gust = easeBetween(time, 11, 18) * (1 - easeBetween(time, 37, 45))
  return { x: 239 + breeze + gust * 4.5, y: 185 + Math.sin(time * .32) * 1.1 }
}

function cubic(points: Cubic, t: number): Point {
  const [a, b, c, d] = points
  const s = 1 - t
  return {
    x: s ** 3 * a.x + 3 * s * s * t * b.x + 3 * s * t * t * c.x + t ** 3 * d.x,
    y: s ** 3 * a.y + 3 * s * s * t * b.y + 3 * s * t * t * c.y + t ** 3 * d.y,
  }
}

function along(segments: Cubic[], progress: number) {
  const position = clamp(progress) * segments.length
  const index = Math.min(segments.length - 1, Math.floor(position))
  return cubic(segments[index], position - index)
}

const seedSpecs = Array.from({ length: 48 }, (_, index) => {
  // Fibonacci distribution over a sphere gives the crown a soft, layered
  // volume instead of placing every parachute on a flat circular wheel.
  const vertical = 1 - 2 * (index + .5) / 48
  const ring = Math.sqrt(1 - vertical * vertical)
  const longitude = index * 2.3999632297
  const depth = Math.cos(longitude) * ring
  const dx = Math.sin(longitude) * ring * 84
  const dy = vertical * 81
  const scale = .57 + (depth + 1) * .15
  return {
    index, dx, dy, depth, scale,
    angle: Math.atan2(dy, dx) * 180 / Math.PI + 90,
    length: Math.max(23, (Math.hypot(dx, dy) - 8) / scale),
    opacity: .48 + (depth + 1) * .24,
    release: index === 0 ? 16 : 18 + ((index * 17) % 48) * .27,
    duration: index === 0 ? 27 : 23 + (index % 5) * 1.4,
    stays: index !== 0 && index % 7 === 0,
  }
}).sort((a, b) => a.depth - b.depth)

type SeedSpec = (typeof seedSpecs)[number]

function attached(seed: SeedSpec, time: number): SeedPose {
  const center = flowerCenter(time)
  const growth = easeBetween(time, 2.6 + seed.index * .025, 7.5 + seed.index * .025)
  const breath = 1 + Math.sin(time * .52) * .012
  return {
    x: center.x + seed.dx * growth * breath,
    y: center.y + seed.dy * growth * breath,
    angle: seed.angle + Math.sin(time * .64 + seed.index) * 1.2,
    scale: seed.scale * mix(.12, 1, growth),
    length: seed.length,
    opacity: seed.opacity * growth,
  }
}

export const dandelionSeeds = seedSpecs.map((seed) => {
  const launch = attached(seed, seed.release)
  const lane = seed.index % 6
  const height = 94 + lane * 12
  const bend = { x: 390 + lane * 10, y: height }
  const turn = { x: 493 - lane * 7, y: 253 + lane * 8 }
  const landing = seed.index === 0 ? { x: 478, y: 394 } : { x: 390 + (seed.index % 9) * 17, y: 350 + (seed.index % 5) * 11 }
  // Shared tangents at the two joins keep each flight continuous. Seeds occupy
  // distinct wind lanes, with a gentle turn and descent instead of sharp loops.
  const route: Cubic[] = [
    [launch, { x: launch.x + 52, y: launch.y - 30 }, { x: bend.x - 59, y: bend.y + 2 }, bend],
    [bend, { x: bend.x + 59, y: bend.y - 2 }, { x: turn.x + 45, y: turn.y - 44 }, turn],
    [turn, { x: turn.x - 45, y: turn.y + 44 }, { x: landing.x - 18, y: landing.y - 50 }, landing],
  ]
  const targetAngle = -19 + (seed.index % 6) * 6
  const angleDelta = ((targetAngle - launch.angle + 540) % 360) - 180
  return { ...seed, launch, route, angleDelta }
})

export function seedPose(seed: (typeof dandelionSeeds)[number], time: number): SeedPose {
  if (time <= seed.release || seed.stays) {
    const pose = attached(seed, time)
    if (seed.stays) pose.opacity *= 1 - easeBetween(time, 43, 65) * .78
    return pose
  }
  const progress = clamp((time - seed.release) / seed.duration)
  // Ease the departure and landing while retaining the same exact launch point.
  const eased = progress * progress * (3 - 2 * progress)
  const position = along(seed.route, eased)
  const unfurl = easeBetween(progress, 0, .3)
  const settle = easeBetween(progress, .80, 1)
  const flutter = Math.sin(progress * Math.PI * 9 + seed.index) * Math.sin(progress * Math.PI) * 4
  return {
    ...position,
    angle: seed.launch.angle + seed.angleDelta * unfurl + flutter,
    scale: seed.launch.scale * mix(1, .67, unfurl) * mix(1, .66, settle),
    length: mix(seed.length, 31 + (seed.index % 4) * 3, unfurl),
    opacity: seed.launch.opacity * (1 - easeBetween(progress, .86, 1)),
  }
}

export function returningSeedPose(index: number, time: number): SeedPose {
  const start = 56 + index * .7
  const progress = clamp((time - start) / 10.5)
  const route: Cubic[] = [
    [{ x: 486 + Math.sin(index * 1.9) * 12, y: 282 }, { x: 469, y: 226 - index * 3 }, { x: 366, y: 229 }, { x: 320, y: 292 }],
    [{ x: 320, y: 292 }, { x: 274, y: 355 }, { x: 253, y: 373 }, { x: 226, y: 399 }],
  ]
  return {
    ...along(route, progress * progress * (3 - 2 * progress)),
    angle: -20 + Math.sin(progress * Math.PI * 3 + index) * 12,
    length: 34,
    scale: .36 * (1 - easeBetween(progress, .75, 1) * .5),
    opacity: easeBetween(time, start, start + 1.3) * (1 - easeBetween(progress, .78, 1)) * .8,
  }
}
