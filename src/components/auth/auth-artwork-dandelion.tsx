import { useRef } from 'react'
import { gsap } from 'gsap'
import { useGSAP } from '@gsap/react'
import {
  DANDELION_DURATION, DANDELION_STILL_TIME, dandelionSeeds, easeBetween,
  flowerCenter, returningSeedPose, sceneOpacity, seedPose, type SeedPose,
} from './auth-dandelion-model'

gsap.registerPlugin(useGSAP)

const leafPath = 'M0 0C-22-8-56-13-108-59L-78-53L-95-75L-64-59L-72-84L-44-62L-49-82L-23-52C-9-37-1-16 0 0Z'
const tuftPath = Array.from({ length: 11 }, (_, ray) => {
  const angle = -Math.PI / 2 + (ray - 5) * .245
  const reach = 20 + Math.cos((ray - 5) * .22) * 5
  const x = Math.cos(angle) * reach
  const y = Math.sin(angle) * reach
  return `M0 0Q${x * .33} ${y * .60} ${x} ${y}M${x * .75} ${y * .78}L${x + (ray - 5) * .38} ${y + 3}`
}).join('')

/** The parachute is the anchor. Its stalk hangs below it, so shortening the
 * stalk during departure never moves or flips the visible crown. */
function SeedGlyph({ length }: { length: number }) {
  return (
    <>
      <path data-seed-stalk="" className="dandelion-stalk" d={`M0 0Q3 ${length * .48} 0 ${length}`} />
      <g className="dandelion-parachute">
        <path d={tuftPath} />
        <path className="dandelion-canopy-edge" d="M-21-11Q-12-27 0-25Q12-27 21-11" />
      </g>
      <g data-seed-body="" transform={`translate(0 ${length})`}>
        <ellipse className="growth-seed" cx="0" cy="0" rx="1.5" ry="4" />
      </g>
    </>
  )
}

function BasalLeaves({ small = false }: { small?: boolean }) {
  const leaves = [
    { angle: -19, side: 1, size: 1 }, { angle: 14, side: 1, size: .82 },
    { angle: -12, side: -1, size: .88 }, { angle: 15, side: -1, size: 1.04 },
  ]
  return (
    <g transform={small ? 'translate(478 405) scale(.55)' : 'translate(226 405)'}>
      {leaves.map(({ angle, side, size }, index) => (
        <g key={index} transform={`rotate(${angle}) scale(${side * size} ${size})`}>
          <g data-basal-leaf={small ? 'young' : 'mother'}>
            <path className={`growth-pigment-${index % 3}`} d={leafPath} />
            <path className="growth-vein" d="M-2-2Q-38-31-91-65M-23-21L-33-49M-45-38L-65-44" />
          </g>
        </g>
      ))}
    </g>
  )
}

interface SeedNodes {
  group: SVGGElement
  stalk: SVGPathElement | null
  body: SVGGElement | null
  hidden: boolean
}

function seedNodes(root: SVGSVGElement, selector: string): SeedNodes[] {
  return Array.from(root.querySelectorAll<SVGGElement>(selector), (group) => ({
    group, stalk: group.querySelector('[data-seed-stalk]'), body: group.querySelector('[data-seed-body]'), hidden: false,
  }))
}

function writeSeed(nodes: SeedNodes, pose: SeedPose) {
  if (pose.opacity < .002) {
    if (!nodes.hidden) nodes.group.setAttribute('opacity', '0')
    nodes.hidden = true
    return
  }
  nodes.hidden = false
  nodes.group.setAttribute('opacity', pose.opacity.toFixed(3))
  nodes.group.setAttribute('transform', `translate(${pose.x.toFixed(2)} ${pose.y.toFixed(2)}) rotate(${pose.angle.toFixed(2)}) scale(${pose.scale.toFixed(3)})`)
  nodes.stalk?.setAttribute('d', `M0 0Q3 ${(pose.length * .48).toFixed(2)} 0 ${pose.length.toFixed(2)}`)
  nodes.body?.setAttribute('transform', `translate(0 ${pose.length.toFixed(2)})`)
}

export default function DandelionArtwork({ seed }: { seed: number }) {
  const rootRef = useRef<SVGSVGElement>(null)

  useGSAP(() => {
    const root = rootRef.current
    if (!root) return
    const particles = seedNodes(root, '[data-dandelion-seed]')
    const returning = seedNodes(root, '[data-dandelion-return]')
    const scene = root.querySelector<SVGGElement>('[data-dandelion-scene]')!
    const stem = root.querySelector<SVGPathElement>('[data-dandelion-stem]')!
    const core = root.querySelector<SVGGElement>('[data-dandelion-core]')!
    const crownVolume = root.querySelector<SVGGElement>('[data-crown-volume]')!
    const mother = root.querySelector<SVGGElement>('[data-dandelion-mother]')!
    const young = root.querySelector<SVGGElement>('[data-dandelion-young]')!
    const youngStem = root.querySelector<SVGPathElement>('[data-young-stem]')!
    const youngCrown = root.querySelector<SVGGElement>('[data-young-crown]')!
    const leaves = Array.from(root.querySelectorAll<SVGGElement>('[data-basal-leaf]'))
    const wind = Array.from(root.querySelectorAll<SVGPathElement>('[data-dandelion-wind]'))

    function render(time: number) {
      const center = flowerCenter(time)
      const growth = easeBetween(time, .2, 5.6)
      scene.setAttribute('opacity', sceneOpacity(time).toFixed(3))
      stem.setAttribute('d', `M226 404C210 335 256 260 ${center.x.toFixed(2)} ${center.y.toFixed(2)}`)
      stem.setAttribute('stroke-dashoffset', (1 - growth).toFixed(4))
      core.setAttribute('transform', `translate(${center.x.toFixed(2)} ${center.y.toFixed(2)}) scale(${(.4 + .6 * growth).toFixed(3)})`)
      core.setAttribute('opacity', easeBetween(time, 3, 6).toFixed(3))
      crownVolume.setAttribute('transform', `translate(${center.x.toFixed(2)} ${center.y.toFixed(2)}) scale(${(.04 + .96 * easeBetween(time, 2.6, 8.7)).toFixed(3)})`)
      crownVolume.setAttribute('opacity', (easeBetween(time, 2.6, 8.7) * (1 - easeBetween(time, 15, 40))).toFixed(3))
      mother.setAttribute('opacity', (1 - easeBetween(time, 44, 58) * .72).toFixed(3))
      particles.forEach((nodes, index) => writeSeed(nodes, seedPose(dandelionSeeds[index], time)))
      returning.forEach((nodes, index) => writeSeed(nodes, returningSeedPose(index, time)))

      const birth = easeBetween(time, 44, 51)
      young.setAttribute('opacity', easeBetween(time, 43, 46).toFixed(3))
      youngStem.setAttribute('stroke-dashoffset', (1 - birth).toFixed(4))
      youngCrown.setAttribute('transform', `translate(486 282) scale(${(.03 + .97 * easeBetween(time, 48, 56)).toFixed(3)})`)
      youngCrown.setAttribute('opacity', easeBetween(time, 48, 51).toFixed(3))
      leaves.forEach((leaf, index) => {
        const at = leaf.dataset.basalLeaf === 'young' ? 46 + (index % 4) * .6 : 1 + index * .5
        const grown = easeBetween(time, at, at + 4)
        const sway = Math.sin(time * .48 + index * .8) * 1.7 * grown
        leaf.setAttribute('transform', `rotate(${sway.toFixed(2)}) scale(${(.025 + .975 * grown).toFixed(3)})`)
        leaf.setAttribute('opacity', grown.toFixed(3))
      })
      wind.forEach((path, index) => {
        const at = 14 + index * 3
        const drawn = easeBetween(time, at, at + 13)
        path.setAttribute('stroke-dashoffset', (1 - drawn).toFixed(4))
        path.setAttribute('opacity', (easeBetween(time, at, at + 4) * (1 - easeBetween(time, at + 15, at + 24)) * .20).toFixed(3))
      })
    }

    // Both reduced motion and the no-animation state use the same valid pose.
    render(DANDELION_STILL_TIME)
    const media = gsap.matchMedia()
    media.add('(prefers-reduced-motion: no-preference)', () => {
      const playhead = { time: 0 }
      const clock = gsap.to(playhead, {
        time: DANDELION_DURATION, duration: DANDELION_DURATION, ease: 'none', repeat: -1,
        paused: true, onUpdate: () => render(playhead.time),
      })
      clock.timeScale(.98 + (seed % 5) * .01)
      // Start with a complete, recognizable flower, then let the breeze arrive.
      clock.seek(DANDELION_STILL_TIME, false)
      let inView = true
      const sync = () => { clock.paused(document.hidden || !inView) }
      const observer = new IntersectionObserver(([entry]) => {
        inView = entry?.isIntersecting ?? true
        sync()
      })
      observer.observe(root)
      document.addEventListener('visibilitychange', sync)
      sync()
      return () => {
        observer.disconnect()
        document.removeEventListener('visibilitychange', sync)
        clock.kill()
        render(DANDELION_STILL_TIME)
      }
    }, root)
    return () => media.revert()
  }, { scope: rootRef, dependencies: [seed], revertOnUpdate: true })

  const stillCenter = flowerCenter(DANDELION_STILL_TIME)
  return (
    <svg ref={rootRef} className="login-growth-art login-dandelion-art" viewBox="0 0 640 480" fill="none" aria-hidden="true" focusable="false">
      <ellipse className="growth-seed" cx="226" cy="410" rx="3" ry="6" transform="rotate(-16 226 410)" />
      <g data-dandelion-scene="">
        <g data-crown-volume="" transform={`translate(${stillCenter.x} ${stillCenter.y})`}>
          <circle className="dandelion-crown-volume" r="82" />
        </g>
        <g data-dandelion-mother="">
          <BasalLeaves />
          <path data-dandelion-stem="" className="growth-stem" pathLength="1" strokeDasharray="1" strokeDashoffset="0" d={`M226 404C210 335 256 260 ${stillCenter.x} ${stillCenter.y}`} />
          <g data-dandelion-core="" transform={`translate(${stillCenter.x} ${stillCenter.y})`}>
            <path className="growth-pigment-1" d="M-7 0Q0-9 8 0Q6 9 0 11Q-6 8-7 0Z" />
            <path className="growth-vein" d="M-5 0L0 8L5 0M0-2V8" />
          </g>
        </g>
        <g className="dandelion-wind" data-detail="">
          <path data-dandelion-wind="" pathLength="1" strokeDasharray="1" opacity="0" d="M307 147C375 94 484 100 510 168C534 232 485 285 426 315" />
          <path data-dandelion-wind="" pathLength="1" strokeDasharray="1" opacity="0" d="M327 164C398 125 476 132 485 191C494 232 460 253 448 278" />
          <path data-dandelion-wind="" pathLength="1" strokeDasharray="1" opacity="0" d="M373 342C414 331 440 351 481 334" />
        </g>
        {dandelionSeeds.map((model) => {
          const pose = seedPose(model, DANDELION_STILL_TIME)
          return (
            <g key={model.index} data-dandelion-seed={model.index} opacity={pose.opacity} transform={`translate(${pose.x} ${pose.y}) rotate(${pose.angle}) scale(${pose.scale})`}>
              <SeedGlyph length={pose.length} />
            </g>
          )
        })}
        <g data-dandelion-young="" opacity="0">
          <BasalLeaves small />
          <path data-young-stem="" className="growth-stem" pathLength="1" strokeDasharray="1" strokeDashoffset="1" d="M478 403C487 361 472 328 486 282" />
          <g data-young-crown="" transform="translate(486 282)">
            {Array.from({ length: 22 }, (_, index) => {
              const angle = index / 22 * Math.PI * 2
              return (
                <g key={index} transform={`translate(${Math.cos(angle) * 32} ${Math.sin(angle) * 32}) rotate(${angle * 180 / Math.PI + 90}) scale(.38)`}>
                  <SeedGlyph length={65} />
                </g>
              )
            })}
            <circle className="growth-pigment-1" r="3" />
          </g>
        </g>
        {Array.from({ length: 7 }, (_, index) => (
          <g key={index} data-dandelion-return={index} opacity="0">
            <SeedGlyph length={34} />
          </g>
        ))}
      </g>
    </svg>
  )
}
