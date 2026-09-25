/** Geometry shared with the choreography. Coordinates are SVG user units;
 * every flight and morph keeps a margin inside the 640 × 480 composition. */
export const leafShapes = {
  bud: 'M0 0C7-8 14-14 20-14C18-7 9-1 0 0Z',
  open: 'M0 0C18-26 53-44 85-30C62-5 24 15 0 0Z',
  wind: 'M0 0C12-37 62-44 85-30C65 0 26 19 0 0Z',
}

type Point = { x: number; y: number }
type Branch = { points: [Point, Point, Point, Point]; at: number }
export const branches: Branch[] = [
  { points: [{ x: 316, y: 329 }, { x: 246, y: 331 }, { x: 164, y: 312 }, { x: 127, y: 276 }], at: 2.8 },
  { points: [{ x: 320, y: 305 }, { x: 386, y: 325 }, { x: 462, y: 294 }, { x: 510, y: 250 }], at: 4 },
  { points: [{ x: 320, y: 268 }, { x: 254, y: 266 }, { x: 160, y: 239 }, { x: 148, y: 188 }], at: 5.8 },
  { points: [{ x: 324, y: 232 }, { x: 386, y: 244 }, { x: 459, y: 206 }, { x: 465, y: 150 }], at: 7.2 },
  { points: [{ x: 323, y: 190 }, { x: 278, y: 192 }, { x: 219, y: 152 }, { x: 221, y: 110 }], at: 8.8 },
  { points: [{ x: 323, y: 151 }, { x: 360, y: 149 }, { x: 391, y: 123 }, { x: 384, y: 84 }], at: 10.2 },
]

function sampleBranch({ points: [a, b, c, d] }: Branch, t: number) {
  const s = 1 - t
  const x = s ** 3 * a.x + 3 * s * s * t * b.x + 3 * s * t * t * c.x + t ** 3 * d.x
  const y = s ** 3 * a.y + 3 * s * s * t * b.y + 3 * s * t * t * c.y + t ** 3 * d.y
  const dx = 3 * s * s * (b.x - a.x) + 6 * s * t * (c.x - b.x) + 3 * t * t * (d.x - c.x)
  const dy = 3 * s * s * (b.y - a.y) + 6 * s * t * (c.y - b.y) + 3 * t * t * (d.y - c.y)
  return { x, y, angle: Math.atan2(dy, dx) * 180 / Math.PI }
}

export function petalShape(length: number, width: number, curl: number) {
  const root = length * .15
  return `M${root} 0C${length * .35} ${-width} ${length * .7} ${-width * (1 + curl)} ${length} ${-width * .22}C${length * 1.04} ${width * .35} ${length * .76} ${width * .65} ${length * .52} ${width * .3}C${length * .3} ${width * .18} ${root + 8} ${width * .15} ${root} 0Z`
}

export const flowerLayers = [
  { count: 12, length: 176, width: 31, offset: -12 },
  { count: 18, length: 119, width: 21, offset: 4 },
  { count: 12, length: 69, width: 19, offset: -6 },
]

function Leaf({ x, y, angle, size = 1, tone = 0, at = 0 }: Point & { angle: number; size?: number; tone?: number; at?: number }) {
  return (
    <g transform={`translate(${x} ${y}) rotate(${angle}) scale(${size})`}>
      <g data-leaf="" data-at={at} className={`growth-pigment-${tone}`}>
        <path data-leaf-surface="" d={leafShapes.open} />
        <path className="growth-leaf-fold" d="M0 0C24-17 55-27 85-30C62-5 24 15 0 0Z" />
        <path data-vein="" className="growth-vein" d="M3-1C26-15 56-25 80-29M22-11L25-26M38-19L43-35M41-19L56-13M59-25L72-19" />
      </g>
    </g>
  )
}

function Rosette({ x, y, size = 1, at = 15 }: Point & { size?: number; at?: number }) {
  return (
    <g transform={`translate(${x} ${y}) scale(${size})`}>
      <g data-rosette="" data-at={at}>
        {Array.from({ length: 9 }, (_, index) => (
          <g key={index} transform={`rotate(${index * 40})`}>
            <path data-rosette-petal="" className={`growth-pigment-${index % 3}`} d="M2 0C5-12 15-21 23-17C30-9 16 0 2 0Z" />
          </g>
        ))}
        <circle className="growth-seed" r="4" />
      </g>
    </g>
  )
}

export function SeedlingScene() {
  return (
    <>
      <ellipse data-cycle-seed="" className="growth-seed" cx="319" cy="394" rx="5" ry="9" transform="rotate(-18 319 394)" />
      <g data-growth-scene="">
        <g className="growth-rootwork" data-roots="">
          {[
            'M319 391C298 405 281 410 241 414M289 406Q278 419 269 427',
            'M319 392C337 410 367 412 394 426M354 410Q372 403 391 407',
            'M319 392C308 412 322 429 307 443M317 424L333 437',
          ].map((d) => <path key={d} data-root="" d={d} />)}
        </g>
        <g data-canopy="">
          <path data-trunk="" className="growth-stem" d="M319 390C305 350 324 311 320 271C316 227 335 192 323 151C316 123 316 107 328 79" />
          {branches.map((branch, index) => {
            const [a, b, c, d] = branch.points
            return (
              <g key={index} data-branch-group={index}>
                <path data-branch={index} className="growth-stem" d={`M${a.x} ${a.y}C${b.x} ${b.y} ${c.x} ${c.y} ${d.x} ${d.y}`} />
                {[.34, .60, .84].flatMap((t, pair) => {
                  const p = sampleBranch(branch, t)
                  return [-1, 1].map((side) => (
                    <Leaf key={`${pair}-${side}`} x={p.x} y={p.y} angle={p.angle + side * 48 + 18}
                      size={.46 + (2 - pair) * .11 - index * .022} tone={(index + pair + (side === 1 ? 1 : 0)) % 3}
                      at={branch.at + 1.3 + t * 2.5 + (side === 1 ? .32 : 0)} />
                  ))
                })}
                <Rosette x={d.x} y={d.y} size={index % 2 ? .62 : .76} at={15 + index * .85} />
              </g>
            )
          })}
          <Leaf x={326} y={100} angle={-58} size={.55} tone={1} at={12} />
          <Leaf x={325} y={104} angle={-139} size={.5} tone={0} at={12.5} />
          <g className="growth-tendril" data-detail="">
            <path data-tendril="" d="M163 263C111 280 80 247 95 222C105 204 128 216 116 232" />
            <path data-tendril="" d="M463 278C519 293 566 265 554 234C547 218 525 223 532 240" />
            <path data-tendril="" d="M239 160C192 153 173 119 188 101C201 84 216 103 204 112" />
          </g>
        </g>
        <g className="growth-ephemeral" data-pollen-stream="">
          {Array.from({ length: 26 }, (_, index) => (
            <g key={index} data-pollen={index}>
              <ellipse className={`growth-pigment-${index % 3}`} rx={index % 3 ? 2 : 3} ry={index % 3 ? 4 : 6} />
            </g>
          ))}
        </g>
        <g className="growth-ephemeral growth-echo-lines" data-orbit-ink="" data-detail="">
          <path data-orbit-line="" d="M122 306C29 178 202 38 366 51C558 66 621 251 490 331" />
          <path data-orbit-line="" d="M139 318C60 200 197 64 365 73C529 82 589 224 514 300" />
        </g>
      </g>
    </>
  )
}

export function FlourishScene() {
  return (
    <>
      <circle data-cycle-seed="" className="growth-seed" cx="320" cy="238" r="6" />
      <g data-growth-scene="">
        <g data-filigree="" className="growth-filigree" data-detail="">
          {Array.from({ length: 36 }, (_, index) => (
            <path key={index} data-lace="" transform={`rotate(${index * 10} 320 238)`}
              d="M350 238C391 212 466 170 510 218C524 237 475 271 437 244C412 226 434 202 454 216" />
          ))}
        </g>
        {flowerLayers.map((layer, ring) => (
          <g key={ring} data-flower-ring={ring}>
            {Array.from({ length: layer.count }, (_, index) => (
              <g key={index} transform={`translate(320 238) rotate(${index * 360 / layer.count + layer.offset})`}>
                <g data-petal="" data-ring={ring} data-petal-index={index} className={`growth-pigment-${(index + ring) % 3}`}>
                  <path data-petal-surface="" d={petalShape(layer.length, layer.width, .5)} />
                  <path data-petal-fold="" className="growth-petal-fold" d={petalShape(layer.length * .96, layer.width * .53, .1)} />
                  <path className="growth-petal-line" d={`M${layer.length * .19} 0Q${layer.length * .6} ${-layer.width * .4} ${layer.length * .93} ${-layer.width * .23}`} />
                </g>
              </g>
            ))}
          </g>
        ))}
        <g data-flower-heart="" className="growth-flower-heart">
          <circle cx="320" cy="238" r="23" />
          <path d="M307 240C306 222 331 221 334 235C337 249 317 255 313 243C310 236 321 230 325 237" />
        </g>
        <g data-phyllotaxis="" className="growth-ephemeral" data-detail="">
          {Array.from({ length: 72 }, (_, index) => {
            const angle = index * 2.399963
            const radius = 9 + Math.sqrt(index) * 13.3
            return <circle key={index} data-flower-grain="" className={`growth-pigment-${index % 3}`} cx={320 + Math.cos(angle) * radius} cy={238 + Math.sin(angle) * radius} r={1.4 + (index % 4) * .25} />
          })}
        </g>
        <g data-flower-satellites="" className="growth-ephemeral">
          {Array.from({ length: 16 }, (_, index) => (
            <g key={index} data-satellite={index}>
              <path className={`growth-pigment-${index % 3}`} d="M0 0C-7-12-1-23 9-26C16-11 9-2 0 0Z" />
              <path className="growth-vein" d="M0 0L8-21" />
            </g>
          ))}
        </g>
      </g>
    </>
  )
}
