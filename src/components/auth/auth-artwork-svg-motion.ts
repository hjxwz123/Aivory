import { gsap } from 'gsap'
import { MotionPathPlugin } from 'gsap/MotionPathPlugin'
import { branches, flowerLayers, leafShapes, petalShape } from './auth-artwork-svg-scenes'

gsap.registerPlugin(MotionPathPlugin)

type Timeline = ReturnType<typeof gsap.timeline>
const elements = <T extends SVGElement = SVGGElement>(root: SVGElement, selector: string) => Array.from(root.querySelectorAll<T>(selector))

/** Fixed chapter lengths keep the story legible; individual phases overlap. */
export const svgStoryDuration = { seedling: 64, flourish: 64 } as const

function drawPath(tl: Timeline, path: SVGPathElement, at: number, duration: number) {
  const length = path.getTotalLength() + 1
  tl.set(path, { strokeDasharray: length, strokeDashoffset: length }, 0)
  tl.to(path, { strokeDashoffset: 0, duration, ease: 'power2.inOut' }, at)
}

function growLeaves(root: SVGElement, tl: Timeline) {
  elements(root, '[data-leaf]').forEach((leaf, index) => {
    const at = Number(leaf.dataset.at)
    const surface = leaf.querySelector('[data-leaf-surface]')
    const vein = leaf.querySelector<SVGPathElement>('[data-vein]')
    tl.set(leaf, { svgOrigin: '0 0', scale: .06, rotation: -31, opacity: 0 }, 0)
    tl.set(surface, { attr: { d: leafShapes.bud } }, 0)
    tl.to(leaf, { scale: 1, rotation: 0, opacity: 1, duration: 2.4, ease: 'power2.out' }, at)
    tl.to(surface, { attr: { d: leafShapes.open }, duration: 3.2, ease: 'power2.out' }, at)
    if (vein) drawPath(tl, vein, at + 1.8, 2.4)
    // Wind travels through the canopy, with contour changes as well as rotation.
    const gust = Math.max(at + 5, 22 + index * .075)
    tl.to(leaf, { rotation: index % 2 ? 9 : -7, duration: 3.8, yoyo: true, repeat: 3 }, gust)
    tl.to(surface, { attr: { d: leafShapes.wind }, duration: 3.8, yoyo: true, repeat: 3 }, gust)
  })
}

function animateSeedling(root: SVGSVGElement, tl: Timeline) {
  tl.addLabel('germination', 0).addLabel('canopy', 12).addLabel('flowering', 18)
    .addLabel('wind', 26).addLabel('dispersal', 36).addLabel('renewal', 54)
  elements<SVGPathElement>(root, '[data-root]').forEach((path, index) => drawPath(tl, path, .5 + index * .65, 4.5))
  elements<SVGPathElement>(root, '[data-trunk]').forEach((path) => drawPath(tl, path, .4, 10.5))
  elements<SVGPathElement>(root, '[data-branch]').forEach((path, index) => drawPath(tl, path, branches[index].at, 4.3))
  growLeaves(root, tl)
  elements<SVGPathElement>(root, '[data-tendril]').forEach((path, index) => drawPath(tl, path, 13 + index * 1.3, 5.6))

  elements(root, '[data-rosette]').forEach((flower) => {
    const at = Number(flower.dataset.at)
    tl.set(flower, { opacity: 0 }, 0)
    tl.to(flower, { opacity: 1, duration: 1.5 }, at)
    elements<SVGPathElement>(flower, '[data-rosette-petal]').forEach((petal, index) => {
      tl.set(petal, { scale: .08, svgOrigin: '0 0', opacity: 0 }, 0)
      tl.to(petal, { scale: 1, opacity: 1, duration: 2.4 }, at + index * .17)
      tl.to(petal, { rotation: 12, scaleY: .82, duration: 4, yoyo: true, repeat: 3 }, 25 + index * .15)
    })
    tl.to(flower, { rotation: 75, svgOrigin: '0 0', duration: 27, ease: 'sine.inOut' }, at)
  })

  const canopy = root.querySelector('[data-canopy]')
  tl.to(canopy, { rotation: 1.4, svgOrigin: '319 390', duration: 5, repeat: 5, yoyo: true }, 18)
  const stream = root.querySelector('[data-pollen-stream]')
  tl.set(stream, { opacity: 1 }, 21)
  elements(root, '[data-pollen]').forEach((pollen, index) => {
    const source = branches[index % branches.length].points[3]
    const at = 21 + index * .48
    const top = 72 + (index % 5) * 13
    const endX = 215 + (index % 8) * 26
    const path = `M${source.x} ${source.y}C${source.x - 30} ${source.y - 42} 184 ${top} 320 ${top}C473 ${top} 558 203 512 285C467 362 ${endX + 45} 347 ${endX} 390`
    tl.set(pollen, { svgOrigin: '0 0', x: source.x, y: source.y, opacity: 0, scale: .65, rotation: index * 17 }, 0)
    tl.to(pollen, { opacity: .82, duration: 1.4 }, at)
    tl.to(pollen, { motionPath: { path }, rotation: '+=230', duration: 18 + (index % 4), ease: 'sine.inOut' }, at)
    tl.to(pollen, { scale: .25, opacity: 0, duration: 3 }, at + 15 + (index % 4))
  })
  const orbit = root.querySelector('[data-orbit-ink]')
  tl.to(orbit, { opacity: 1, duration: 3 }, 23)
  elements<SVGPathElement>(root, '[data-orbit-line]').forEach((path, index) => drawPath(tl, path, 23 + index * 2, 10))
  tl.to(orbit, { opacity: 0, duration: 7 }, 42)

  // The canopy passes into a quiet afterimage in layers; the seed remains.
  elements(root, '[data-branch-group]').reverse().forEach((branch, index) => {
    tl.to(branch, { opacity: .25, duration: 3.5 }, 48 + index * .6)
    tl.to(branch, { opacity: 0, duration: 4 }, 52 + index * .6)
  })
  tl.to(root.querySelector('[data-roots]'), { opacity: .15, duration: 7 }, 53)
  tl.to(root.querySelector('[data-growth-scene]'), { opacity: 0, duration: 4 }, 60)
}

function animateFlourish(root: SVGSVGElement, tl: Timeline) {
  tl.addLabel('bud', 0).addLabel('unfurl', 8).addLabel('woven-flower', 19)
    .addLabel('expansion', 31).addLabel('seed-spiral', 43).addLabel('gather', 54)
  elements(root, '[data-flower-ring]').forEach((ring, index) => {
    const direction = index % 2 ? -1 : 1
    tl.set(ring, { svgOrigin: '320 238' }, 0)
    tl.to(ring, { rotation: direction * 45, duration: 15 }, 1)
    tl.to(ring, { rotation: direction * 132, duration: 18 }, 16)
    tl.to(ring, { rotation: direction * 222, duration: 18 }, 34)
    tl.to(ring, { rotation: direction * 270, scale: .12, opacity: 0, duration: 9, ease: 'power2.inOut' }, 53)
  })
  elements(root, '[data-petal]').forEach((petal) => {
    const ring = Number(petal.dataset.ring)
    const index = Number(petal.dataset.petalIndex)
    const layer = flowerLayers[ring]
    const surface = petal.querySelector('[data-petal-surface]')
    const fold = petal.querySelector('[data-petal-fold]')
    const at = .7 + ring * 2.3 + index * .2
    tl.set(petal, { svgOrigin: '0 0', scaleX: .12, scaleY: .25, rotation: -45, opacity: 0 }, 0)
    tl.to(petal, { scaleX: 1, scaleY: 1, rotation: 0, opacity: 1, duration: 4.8, ease: 'power2.out' }, at)
    tl.to(surface, { attr: { d: petalShape(layer.length, layer.width * 1.65, -.12) }, duration: 7 }, 12 + index * .11)
    tl.to(fold, { attr: { d: petalShape(layer.length * .97, layer.width * .84, .2) }, duration: 7 }, 12 + index * .11)
    // Alternating fans part to reveal the counter-rotating ink lattice.
    tl.to(petal, { x: ring === 0 ? 13 : 7, rotation: 13, scaleY: .65, opacity: ring === 1 ? .76 : .9, duration: 7 }, 25 + index * .16)
    tl.to(surface, { attr: { d: petalShape(layer.length * .95, layer.width * .72, 1.45) }, duration: 8 }, 29 + index * .1)
    tl.to(petal, { x: 0, scaleY: 1.08, rotation: -9, duration: 8 }, 38 + index * .15)
    tl.to(surface, { attr: { d: petalShape(layer.length, layer.width * 1.3, .35) }, duration: 7 }, 41 + index * .09)
  })

  const lace = root.querySelector('[data-filigree]')
  tl.set(lace, { opacity: 0, svgOrigin: '320 238', scale: .78 }, 0)
  tl.to(lace, { opacity: 1, scale: 1, duration: 8 }, 13)
  tl.to(lace, { rotation: -80, duration: 35, ease: 'none' }, 15)
  elements<SVGPathElement>(root, '[data-lace]').forEach((path, index) => drawPath(tl, path, 13 + index * .12, 6.5))
  tl.to(lace, { scale: .3, opacity: 0, duration: 9 }, 49)

  const spiral = root.querySelector('[data-phyllotaxis]')
  tl.set(spiral, { svgOrigin: '320 238', scale: .25, rotation: -100 }, 0)
  tl.to(spiral, { opacity: .95, scale: 1.38, rotation: 25, duration: 9 }, 32)
  tl.to(spiral, { rotation: 170, scale: 1.18, duration: 13 }, 41)
  tl.to(spiral, { rotation: 260, scale: .04, opacity: 0, duration: 8 }, 54)
  elements<SVGCircleElement>(root, '[data-flower-grain]').forEach((grain, index) => {
    tl.fromTo(grain, { opacity: .2 }, { opacity: 1, duration: 2.5, repeat: 3, yoyo: true }, 35 + index * .035)
  })

  const satellites = root.querySelector('[data-flower-satellites]')
  tl.set(satellites, { opacity: 1 }, 19)
  elements(root, '[data-satellite]').forEach((satellite, index) => {
    const angle = index / 16 * Math.PI * 2
    const start = { x: 320 + Math.cos(angle) * 150, y: 238 + Math.sin(angle) * 150 * .73 }
    const path = Array.from({ length: 15 }, (_, step) => {
      const progress = step / 14
      const a = angle + progress * Math.PI * 1.8
      const radius = 150 + Math.sin(progress * Math.PI) * 70
      return { x: 320 + Math.cos(a) * radius, y: 238 + Math.sin(a) * radius * .73 }
    })
    tl.set(satellite, { svgOrigin: '0 0', x: start.x, y: start.y, opacity: 0, scale: .48, rotation: index * 22.5 }, 0)
    tl.to(satellite, { opacity: .75, duration: 2 }, 19 + index * .43)
    tl.to(satellite, { motionPath: { path, curviness: .6 }, rotation: '+=180', duration: 23, ease: 'sine.inOut' }, 19 + index * .43)
    tl.to(satellite, { opacity: 0, scale: .15, duration: 4 }, 38 + index * .43)
  })
  tl.to(root.querySelector('[data-flower-heart]'), { rotation: 160, svgOrigin: '320 238', duration: 53, ease: 'none' }, 1)
  tl.to(root.querySelector('[data-flower-heart]'), { scale: .26, opacity: 0, duration: 8 }, 54)
  tl.to(root.querySelector('[data-growth-scene]'), { opacity: 0, duration: 2 }, 62)
}

export const animateSvgStory = { seedling: animateSeedling, flourish: animateFlourish }
