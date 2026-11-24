import type { IllustratedScene } from '@/types/settings'
import type { Brush } from './scene-brush'

type Sketch = (b: Brush) => void

function potteryShelf({ path, line, oval, at, p }: Brush) {
  path('M4 59 Q74 55 187 56 L189 60 L165 64 L5 65 Z', p.gold, p.shade)
  line('M13 62 Q81 59 154 60 M28 65 l2 5 8 -6 M149 63 l1 5 6 -6', p.shade)
  at(11, 4, .92, 0, () => {
    path('M6 31 Q28 37 54 29 Q51 48 37 51 L34 56 Q25 58 17 54 L17 51 Q9 46 6 31 Z', p.color)
    oval(30, 31, 24, 4.5, p.white)
    line('M10 38 Q30 45 50 36 M14 43 Q30 49 47 42', p.shade, 1.5)
  })
  path('M79 22 L79 13 Q88 10 96 14 L95 24 C95 32 112 32 111 44 Q110 56 89 57 Q68 58 66 47 Q63 37 73 31 Z', p.pale)
  oval(87, 13, 9, 2.5, p.gold)
  path('M67 40 Q88 48 109 38 L111 44 Q90 54 66 45 Z', p.shade, null)
  line('M71 38 q7 -4 8 -10 M76 50 q10 5 19 3', p.white, 1.7)
  path('M125 33 Q136 30 150 32 L150 50 Q144 60 127 54 Z', p.gold)
  path('M150 35 Q165 30 162 42 Q158 49 150 48 L150 44 Q159 43 157 37 L150 39 Z', p.gold)
  oval(137, 33, 12.5, 3, p.white)
  line('M131 38 v10 M137 39 l1 8 M144 38 v8', p.shade, 1)
}

function recordBin({ path, line, oval, at, p }: Brush) {
  at(117, 52, 1, 0, () => {
    oval(0, 0, 46, 46, p.ink, p.lead)
    for (const r of [42, 38, 34, 29, 23]) oval(0, 0, r, r, null, p.lead, .75)
    path('M-32 -31 Q-23 -40 -9 -42 L-2 -18 L-11 -15 Z M15 12 L36 20 L29 32 Z', p.color, null)
    oval(0, 0, 14, 14, p.gold)
    oval(0, 0, 2.6, 2.6, p.white)
  })
  path('M22 32 L80 14 L101 66 L41 82 Z', p.gold)
  path('M29 34 L78 20 L95 63 L45 78 Z', p.shade)
  path('M33 46 Q50 27 64 34 T85 34 L88 43 Q76 55 62 46 Q50 37 38 55 Z', p.pale, null)
  line('M38 38 l20 -6 M41 63 l12 -4', p.white, 1.2)
  path('M3 60 L164 54 L176 61 L160 74 H8 Z', p.color, p.ink)
  line('M8 63 L161 58', p.pale, 2)
}

function photoLine({ path, line, oval, at, p }: Brush) {
  line('M-2 9 Q86 33 209 4', p.lead, 1.5)
  for (const [x, y, r] of [[30, 17, -11], [91, 19, 6], [150, 14, -5]]) {
    at(x, y, 1, r, () => {
      path('M0 0 l43 -1 1 41 -44 2 Z', p.white, p.shade, 1)
      path('M4 5 L39 4 L40 32 L4 34 Z', p.color, null)
      if (r < -6) {
        path('M4 28 L13 15 L19 22 L25 11 L39 26 L40 32 L4 34 Z', p.shade, null)
        oval(10, 12, 3, 3, p.gold, null)
      } else if (r > 0) {
        path('M4 24 L12 24 L12 16 L19 16 L19 10 L23 10 L24 17 L30 17 L30 22 L39 22 L40 32 L4 34 Z', p.ink, null)
        line('M15 19 v2 M27 20 v2', p.gold)
      } else {
        oval(22, 19, 10, 10, p.pale)
        line('M5 29 Q15 20 22 22 T40 14', p.shade, 2)
      }
      path('M17 -6 l4 -1 1 11 -4 1 Z', p.gold, p.ink, .8)
    })
  }
}

export function paintSidebarOrnament(b: Brush, scene: IllustratedScene, w: number, h: number, companion: Sketch) {
  const { at, path, line, oval, p } = b
  const scale = Math.min(w / 208, h / 70, 1.3)
  at((w - 208 * scale) / 2, (h - 70 * scale) / 2, scale, 0, () => {
    switch (scene) {
      case 'pottery':
        potteryShelf(b)
        break
      case 'vinyl':
        recordBin(b)
        break
      case 'observatory':
        // An engraved planisphere with its pivot, degree marks and folding chart.
        oval(151, 37, 30, 30, p.gold, p.shade, 1.4)
        oval(151, 37, 25, 25, p.shade)
        oval(151, 37, 17, 17, null, p.color, .9)
        for (let i = 0; i < 24; i++) {
          at(151, 37, 1, i * 15, () => line(i % 3 === 0 ? 'M0 -28 v4' : 'M0 -28 v2', p.ink, .9))
        }
        line('M137 44 L144 36 L153 39 L163 23 L169 31 M128 46 Q146 32 174 28', p.white, .9)
        for (const [x, y] of [[137, 44], [144, 36], [153, 39], [163, 23], [169, 31]]) oval(x, y, 1.6, 1.6, p.gold, null)
        oval(151, 37, 2.4, 2.4, p.gold, null)
        at(49, 2, .43, 0, () => companion(b))
        break
      case 'postroom':
        at(36, -13, .48, -10, () => companion(b))
        path('M53 39 L100 49 L146 30 L146 58 L55 68 Z', p.color)
        path('M54 41 L99 53 L144 33', undefined, p.white, 1.3)
        for (const x of [64, 74, 84, 94, 104, 114, 124, 134]) {
          line(`M${x} ${63 - (x - 64) * .10} l4 -5`, x % 20 === 4 ? p.pale : p.shade, 2.5)
        }
        at(149, 12, .85, 9, () => {
          oval(0, 0, 11, 11, null, p.shade, 1.2)
          oval(0, 0, 8, 8, null, p.lead, .8)
          line('M-4 0 l3 2 5 -5 M8 -2 q6 -4 13 -1 t12 -1 M9 3 q6 -4 13 -1 t11 -1', p.shade, 1.3)
        })
        break
      case 'bathysphere':
        // The winch pays out a line that changes depth across a ruled sounder chart.
        at(-3, 9, .44, 0, () => companion(b))
        path('M79 13 Q99 6 118 24 T158 24 Q175 4 197 18 M80 18 Q99 11 118 29 T158 29 Q177 10 198 24', undefined, p.color, 1.15)
        line('M78 48 Q103 31 129 47 T198 40', p.lead, 1)
        line('M83 58 h109 M83 57 v-5 M98 57 v-3 M113 57 v-5 M128 57 v-3 M143 57 v-5 M158 57 v-3 M173 57 v-5 M188 57 v-3', p.shade, .9)
        line('M148 20 v25', p.shade, 1.5)
        path('M143 46 q0 -6 5 -7 q6 1 6 7 l-3 3 h-6 Z', p.gold)
        break
      case 'darkroom':
        photoLine(b)
        break
    }
  })
}

// The editor's usable text and tool area starts inside its perimeter. Keep
// every inked edge inside the rounded shell, including when the draft grows.
export function paintComposerOrnament(b: Brush, scene: IllustratedScene, w: number, h: number) {
  const { path, line, oval, at, p } = b
  switch (scene) {
    case 'pottery':
      path(`M0 0 Q10 ${h * .18} 6 ${h * .35} T8 ${h * .63} Q11 ${h * .78} 3 ${h} H0 Z`, p.color, null)
      line(`M2 12 Q8 ${h * .30} 3 ${h * .47} T5 ${h * .78}`, p.gold, 1.6)
      for (const [x, y, r] of [[8, .29, 1.2], [10, .36, 1.7], [5, .51, 1], [9, .68, 1.3], [11, .74, 1]]) oval(x, y * h, r, r, p.shade, null)
      break
    case 'vinyl': {
      // Only the exposed arc of the record protrudes from its paper sleeve.
      const centerY = h - 46
      oval(w * .58, centerY, 45, 45, p.ink, p.shade)
      for (const r of [42, 39]) oval(w * .58, centerY, r, r, null, p.lead, 1)
      path(`M${w * .58 - 23} 0 l6 5 5 1 -7 -6 Z`, p.color, null)
      line(`M2 5 L${w * .58 - 40} 5 M${w * .58 + 40} 5 h17`, p.color, 1.3)
      break
    }
    case 'observatory':
      line('M1 6 Q30 12 57 6 T113 4 Q130 7 143 5', p.lead, .9)
      line('M24 5 L44 8 L70 2 L91 8 L111 3', p.shade, 1.1)
      for (const [x, y, r] of [[24, 5, 1.7], [44, 8, 2.4], [70, 2, 2.3], [111, 3, 1.6]]) oval(x, y, r, r, p.color, p.shade, .65)
      path('M91 1 Q92 6 97 8 Q92 9 91 14 Q90 9 85 8 Q90 7 91 1 Z', p.gold, p.shade, .75)
      break
    case 'postroom':
      // A clipped paper fold and just one run of airmail edging.
      path(`M${w - 36} 0 H${w} V31 Z`, p.pale, null)
      path(`M${w - 33} 0 Q${w - 15} 4 ${w - 14} 15 L${w} 29 Z`, p.white, p.lead, .9)
      for (let x = 7; x < w - 32; x += 16) {
        path(`M${x} 1 h6 l-3 4 h-6 Z`, p.color, null)
        path(`M${x + 8} 1 h5 l-3 4 h-5 Z`, p.lead, null)
      }
      break
    case 'bathysphere':
      line(`M${w - 3} 2 V${h - 2}`, p.shade, 1.3)
      for (let y = 5; y < h - 2; y += 5) {
        const length = y % 15 === 5 ? 7 : 3.6
        line(`M${w - 3} ${y} h-${length}`, p.lead, 1)
      }
      path(`M1 ${h * .55 - 3} l6 3 -6 3 Z`, p.gold, p.shade, .8)
      break
    case 'darkroom':
      for (const [x, y, angle] of [[9, 9, 0], [w - 9, 9, 90], [w - 9, h - 9, 180], [9, h - 9, 270]]) {
        at(x, y, 1, angle, () => line('M0 7 V0 H12', p.lead, 1.35))
      }
      line(`M${w * .5 - 12} 4 h24 m4 0 h3`, p.gold, 1.25)
      break
  }
}
