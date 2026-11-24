import type { IllustratedScene } from '@/types/settings'
import { brush, seededRandom, type Brush, type Palette } from './scene-brush'
import { paintPanorama } from './home-scene-landscapes'
import { paintComposerOrnament, paintSidebarOrnament } from './home-scene-ornaments'

const palettes: Record<IllustratedScene, { light: Palette; dark: Palette }> = {
  pottery: {
    light: { paper: '#fcf4eb', ink: '#4b3436', lead: '#99716a', pale: '#f0d6be', color: '#dd805b', shade: '#ab5140', gold: '#e7ae63', white: '#fff7df' },
    dark: { paper: '#251e21', ink: '#321e24', lead: '#b48d84', pale: '#695051', color: '#c47a61', shade: '#985649', gold: '#c59a65', white: '#dcc5ab' },
  },
  vinyl: {
    light: { paper: '#f2f5fc', ink: '#293858', lead: '#7d8daa', pale: '#d8e2f2', color: '#819cd5', shade: '#526faa', gold: '#ef9d7c', white: '#fff1d1' },
    dark: { paper: '#191e2b', ink: '#182035', lead: '#8795b2', pale: '#495673', color: '#778bbd', shade: '#526796', gold: '#d18f79', white: '#d9cdb9' },
  },
  observatory: {
    light: { paper: '#f6f2fb', ink: '#383050', lead: '#9988a9', pale: '#e1d6ed', color: '#a091ce', shade: '#72649d', gold: '#edbb72', white: '#fff1d9' },
    dark: { paper: '#211e30', ink: '#292239', lead: '#a093b4', pale: '#564767', color: '#9483b5', shade: '#6c5e8a', gold: '#d9aa70', white: '#d6ccdd' },
  },
  postroom: {
    light: { paper: '#fff5f0', ink: '#522e41', lead: '#a16f79', pale: '#f0d9cc', color: '#cf6776', shade: '#99455c', gold: '#d9a05b', white: '#fff0d8' },
    dark: { paper: '#291f27', ink: '#3b2535', lead: '#b98c95', pale: '#654951', color: '#ca7787', shade: '#984c69', gold: '#cfaa78', white: '#e5cbbd' },
  },
  bathysphere: {
    light: { paper: '#f0f6fb', ink: '#273c55', lead: '#7295ac', pale: '#d5e4f0', color: '#94b9d1', shade: '#487899', gold: '#dfa85d', white: '#faf2d8' },
    dark: { paper: '#19232e', ink: '#223344', lead: '#91acbf', pale: '#43576c', color: '#8aaec5', shade: '#4d718d', gold: '#cda275', white: '#d9d4c5' },
  },
  darkroom: {
    light: { paper: '#f6f3ee', ink: '#353039', lead: '#8d8084', pale: '#e2d9cf', color: '#928897', shade: '#625a68', gold: '#d39a68', white: '#fff2dd' },
    dark: { paper: '#232227', ink: '#2a272e', lead: '#a69b9e', pale: '#514b55', color: '#958997', shade: '#645d6d', gold: '#c79369', white: '#d9cec1' },
  },
}

// A slip-painted jug on a banding wheel, with a loop handle and cobalt glaze marks.
function pottery({ path, line, oval, p }: Brush) {
  const cobalt = '#526887'
  oval(173, 242, 127, 6, p.pale, null)
  path('M49 212 Q165 199 281 211 L276 222 Q168 236 54 225 Z', p.shade)
  path('M127 220 L122 233 Q158 243 205 233 L199 220', p.color)
  oval(164, 234, 43, 6, p.color)
  oval(164, 213, 116, 10, p.gold)
  oval(165, 212, 98, 6, null, p.shade, .9)
  line('M65 222 Q136 230 200 227 M215 226 L245 223', p.white, .9)
  path('M210 103 C269 64 279 125 252 155 C244 166 230 172 216 177 L213 159 C248 148 257 117 249 109 Q240 101 218 122 Z', p.color)
  line('M223 101 C260 81 268 113 256 138 M251 146 Q244 158 228 165', p.white, 2)
  path('M116 61 Q144 69 173 66 L191 53 Q197 59 192 71 L187 82 C183 100 194 111 211 123 C239 146 237 177 216 196 Q206 205 196 207 L193 213 Q168 219 139 212 L137 206 C100 194 81 163 96 134 C103 120 123 107 128 91 Q133 79 116 68 Z', p.color, p.ink, 2.1)
  path('M117 60 C140 55 158 67 187 54 Q195 49 192 57 Q185 70 167 71 Q139 73 118 67 Z', p.white)
  line('M127 64 Q148 62 165 67 M131 78 Q156 86 185 75', p.shade)
  path('M130 86 Q158 92 186 81 L186 90 Q155 102 129 94 Z', p.gold, null)
  path('M95 139 Q160 166 222 136 Q229 146 230 155 Q161 183 92 156 Q91 147 95 139 Z', p.white, null)
  line('M95 137 Q164 166 221 134 M92 159 Q163 186 230 157', p.shade, 1.2)
  for (let i = 0; i < 10; i++) {
    const x = 98 + i * 13.4, y = 142 + 13 * Math.sin(i * Math.PI / 9)
    path(`M${x} ${y + 6} q1 -9 5 -7 q4 1 2 6 q-3 6 -7 6 Z`, cobalt, null)
  }
  line('M115 125 Q126 115 133 102 M109 131 l-3 5 M103 169 Q111 189 128 195 M141 207 Q169 213 194 205', p.white, 2.4)
  for (let i = 0; i < 7; i++) line(`M${195 + i * 3} ${195 - i * 1.6} q6 -7 10 -16`, p.shade, 1.1)
  line('M113 202 Q153 222 204 213 M44 230 l38 3 M225 236 l31 -2', p.lead, .8)
}

function potteryTools({ path, line, oval, at, p }: Brush) {
  oval(101, 144, 82, 4, p.pale, null)
  at(35, 18, .94, -8, () => {
    path('M22 79 L14 20 Q14 15 17 16 L30 80 Z', p.gold)
    path('M15 28 C5 26 5 15 6 3 Q16 9 18 25 Z', p.shade)
    path('M35 80 L45 12 Q48 8 50 14 L42 83 Z', p.color)
    path('M45 14 Q44 1 51 -4 Q54 7 50 15 Z', p.ink)
    path('M47 86 L66 35 L70 33 L54 88 Z', p.white)
    path('M12 71 Q37 77 62 69 L54 117 Q38 126 22 118 Z', p.white)
    line('M20 86 Q37 93 57 85 M22 91 Q38 98 56 90', '#526887', 2.8)
  })
  path('M91 115 Q135 122 169 112 Q164 131 141 134 L139 140 Q124 143 113 139 L113 135 Q95 130 91 115 Z', p.color)
  oval(130, 114, 39, 7, p.white)
  line('M99 115 Q129 106 159 114 M99 125 Q129 136 157 125', p.shade)
  line('M26 148 l28 1 M146 146 l22 -1', p.lead)
}

// The lid shows the inside of a portable record player, with its platter in perspective.
function vinyl({ path, line, oval, at, p }: Brush) {
  oval(161, 235, 140, 8, p.pale, null)
  path('M48 138 L61 41 Q62 33 72 35 L248 54 Q255 55 253 63 L237 153 Z', p.shade, p.ink, 2.3)
  path('M56 131 L69 46 Q70 42 76 43 L243 61 L231 143 Z', p.color)
  path('M68 121 L77 56 L229 72 L219 132 Z', p.pale)
  line('M72 124 L220 139 M80 60 l8 1 M214 77 l9 1', p.white, 1.5)
  at(141, 90, 1, 7, () => {
    for (let i = 0; i < 11; i++) {
      const h = [3, 7, 5, 10, 15, 7, 11, 18, 9, 5, 3][i]
      line(`M${i * 3.7 - 18} ${-h / 2} v${h}`, p.shade, 1.4)
    }
  })
  path('M48 135 L237 149 L292 193 L281 213 L89 229 L27 176 Z', p.shade, p.ink, 2.2)
  path('M27 175 L88 213 L292 191 L281 216 L89 238 L28 194 Z', p.color)
  path('M46 133 L236 145 L292 191 L88 214 L27 175 Z', p.white, p.ink, 2)
  path('M46 139 L80 143 L59 176 L38 169 Z', p.gold, null)
  oval(148, 170, 71, 25, p.ink)
  for (const radius of [64, 58, 49, 42, 35]) oval(148, 169, radius, radius * .326, null, '#73839f', .8)
  path('M91 155 Q109 147 124 147 L139 163 L131 165 Z M169 175 L210 177 Q205 184 192 187 Z', p.pale, null)
  oval(148, 169, 20, 7, p.gold)
  oval(148, 168, 2.4, 1.7, p.ink)
  oval(222, 153, 11, 4.6, p.shade)
  line('M223 151 L240 166 Q244 170 237 173 L201 183', p.ink, 5.4)
  line('M223 150 L240 165 Q244 169 237 171 L201 181', p.color, 2.6)
  path('M194 180 L204 178 L207 182 L195 187 L190 185 Z', p.gold)
  oval(264, 188, 5, 2.4, p.shade)
  line('M87 219 L282 197 M40 184 l32 19', p.white)
  path('M151 219 L153 226 Q156 230 164 229 L193 225 Q200 224 200 216 L195 216 Q196 221 190 221 L164 224 Q159 224 158 218 Z', p.gold)
  for (const x of [111, 241]) path(`M${x} ${x === 111 ? 213 : 200} l8 -1 -1 8 -6 1 Z`, p.gold)
}

function sleeves({ path, line, oval, at, p }: Brush) {
  oval(104, 147, 86, 5, p.pale, null)
  at(20, 15, .94, -7, () => {
    path('M9 36 L99 24 L109 126 L13 135 Z', p.shade)
    path('M28 25 L118 30 L115 125 L20 123 Z', p.white)
    oval(79, 54, 44, 43, p.ink)
    oval(79, 54, 38, 37, null, p.lead, .9)
    oval(79, 54, 28, 27, null, p.lead, .9)
    oval(79, 54, 12, 12, p.gold)
    path('M22 56 L122 54 L127 140 L23 138 Z', p.gold)
    path('M23 121 Q45 75 70 83 Q92 90 118 62 L122 83 Q99 111 78 102 Q51 86 31 137 L24 137 Z', p.white, null)
    path('M51 138 Q74 104 93 112 Q108 117 124 95 L125 107 Q111 129 95 124 Q81 117 65 139 Z', p.shade, null)
    line('M28 59 l21 -1 M28 63 l12 0 M105 132 l13 -1', p.ink, 1.1)
  })
}

function star(b: Brush, x: number, y: number, size: number) {
  b.at(x, y, size, 0, () => b.path('M0 -7 Q1 -1 6 0 Q1 1 0 7 Q-1 1 -6 0 Q-1 -1 0 -7 Z', b.p.gold, b.p.ink, .8))
}

function telescope(b: Brush) {
  const { path, line, oval, at, p } = b
  // The three tripod legs meet a brass equatorial mount.
  path('M150 145 L161 147 L108 234 L100 235 Z M167 145 L176 142 L222 228 L214 229 Z', p.gold)
  path('M160 145 L169 145 L166 220 L161 222 Z', p.shade)
  line('M107 230 L144 165 M215 221 L182 158 M117 207 L166 187 L204 200', p.ink)
  path('M146 141 L152 127 L166 128 L174 139 L178 146 Q165 154 146 147 Z', p.color)
  path('M154 136 Q146 125 151 111 L162 106 L173 113 L169 129 L163 137 Z', p.gold)
  oval(160, 119, 7, 7, p.shade)
  at(158, 110, 1, -34, () => {
    path('M-86 -8 L-55 -8 L-54 7 L-86 7 Z', p.gold)
    path('M-95 -10 L-85 -10 L-84 9 L-94 10 Z', p.shade)
    path('M-57 -15 Q-12 -19 70 -21 L73 18 L-56 14 Q-62 0 -57 -15 Z', p.color, p.ink, 2)
    path('M-55 4 Q1 10 72 4 L72 18 L-55 14 Z', p.shade, null)
    path('M-53 -15 L-44 -16 L-42 15 L-51 14 Z M-9 -17 L-2 -18 L0 15 L-7 15 Z', p.gold)
    path('M57 -22 L75 -23 L76 21 L58 19 Z', p.gold)
    oval(75, -1, 8, 23, p.ink)
    oval(76, -1, 4.6, 18, p.shade, p.white, .8)
    line('M-36 -11 L-17 -12 M6 -13 L45 -15 M76 -13 Q78 -6 77 -3', p.white, 2)
    path('M-21 -19 L-19 -28 L-13 -28 L-13 -19 M22 -19 L22 -30 L27 -30 L29 -21', p.gold)
    path('M-29 -30 L38 -34 L39 -27 L-28 -24 Z', p.shade)
    line('M-89 -6 v9 M-86 -6 v10 M61 -17 l1 29', p.ink, 1)
  })
  star(b, 262, 60, 1)
  star(b, 234, 90, .55)
  line('M50 217 l12 -3 M70 217 l4 -1', p.lead)
}

function starChart(b: Brush) {
  const { path, line, oval, at, p } = b
  at(17, 41, 1, -12, () => {
    path('M0 24 L90 10 L144 27 L147 120 L88 107 L-1 119 Z', p.pale)
    path('M0 19 L91 7 L148 22 L147 113 L86 101 L-2 114 Z', p.color)
    path('M7 25 L83 15 L80 95 L4 105 Z', p.shade)
    const stars = [[16, 78], [33, 63], [43, 65], [55, 46], [69, 51]]
    line('M16 78 L33 63 L43 65 L55 46 L69 51', p.white, 1)
    for (const [x, y] of stars) oval(x, y, 2, 2, p.gold, null)
    line('M11 95 l13 -2 M30 92 l6 -1 M101 29 l32 7 M99 34 l22 5 M97 80 l35 8 M97 85 l26 6', p.ink)
    oval(117, 59, 11, 11, p.gold)
    line('M99 66 Q111 62 132 50', p.ink, 2)
    line('M89 13 L86 97', p.white)
  })
  star(b, 149, 35, .8)
}

// The enamel pillar box has a cast-iron crown, a brass collection plate and a fluted base.
function postbox({ path, line, oval, p }: Brush) {
  oval(173, 239, 85, 6, p.pale, null)
  path('M111 214 L222 211 L230 229 Q172 244 100 231 Z', p.shade, p.ink, 2)
  path('M113 81 Q165 69 218 77 L220 214 Q166 228 111 215 Z', p.color, p.ink, 2)
  path('M196 77 Q210 74 218 77 L220 214 Q211 217 200 217 Z', p.shade, null)
  path('M115 177 Q162 187 218 174 L220 212 Q170 225 112 214 Z', p.shade)
  for (let i = 0; i < 13; i++) {
    const x = 117 + i * 7.7, y = 178 + 4 * Math.sin(i * Math.PI / 12)
    line(`M${x} ${y + 5} l-1 22`, p.color, 1.7)
  }
  path('M101 76 Q100 70 108 67 L114 63 Q119 39 146 36 Q151 26 165 25 Q182 26 190 37 Q208 42 216 61 L227 66 Q233 70 229 76 Q171 93 101 78 Z', p.color, p.ink, 2.1)
  path('M105 69 Q166 80 224 65 L229 72 Q170 89 103 76 Z', p.shade)
  path('M157 27 Q157 21 165 17 Q175 21 174 27 Z', p.gold)
  line('M124 60 Q129 46 146 41 M153 38 Q147 48 145 60 M186 43 Q196 50 201 60', p.white, 1.6)
  path('M128 98 Q161 91 199 94 L202 103 L195 107 L127 109 L122 104 Z', p.shade)
  path('M131 101 L192 98 L190 104 L132 107 Z', p.ink)
  path('M139 120 L185 117 L185 157 L139 162 Z', p.gold)
  path('M143 124 L180 121 L181 151 L143 156 Z', p.white)
  line('M149 131 l22 -2 M150 136 l24 -2 M150 143 l13 -1 M150 147 l21 -2', p.lead, 1.15)
  oval(193, 150, 2.5, 3.5, p.gold)
  line('M122 113 L122 170 M127 172 Q161 179 205 168 M110 226 Q162 238 218 226', p.white, 1.5)
}

function letters({ at, path, line, oval, p }: Brush) {
  oval(107, 143, 90, 5, p.pale, null)
  at(28, 64, .93, -8, () => {
    path('M0 31 L121 20 L144 79 L21 94 Z', p.color)
    path('M4 23 L121 16 L140 74 L20 87 Z', p.white)
    path('M5 23 L64 53 Q69 57 74 52 L121 16 M19 86 L52 52 M137 73 L88 45', undefined, p.lead)
    for (let i = 0; i < 9; i++) line(`M${20 + i * 12} ${84 - i * 1.14} l5 -1`, i % 2 ? p.color : p.shade, 2)
  })
  at(93, 39, .8, 12, () => {
    path('M0 22 L87 12 L95 71 L6 83 Z', p.white)
    path('M60 22 l18 -2 2 18 -18 2 Z', p.color)
    line('M20 49 l35 -4 M21 55 l28 -3 M23 62 l22 -2', p.lead)
    oval(61, 38, 12, 12, null, p.shade, 1)
    line('M62 34 q8 -4 19 -3 M64 39 q7 -4 18 -3', p.shade)
  })
}

// Riveted diving bell with a strapped porthole, lifting eye and weighted landing frame.
function divingBell({ path, line, oval, p, at }: Brush) {
  path('M119 190 L109 226 L122 228 L138 200 M206 194 L219 228 L232 228 L221 187', p.shade)
  path('M103 224 Q164 231 234 223 L245 228 L242 234 Q170 245 101 231 Z', p.gold)
  path('M111 110 Q118 74 157 62 Q206 50 231 85 Q259 124 246 173 Q236 207 196 213 Q136 223 111 191 Q88 156 111 110 Z', p.gold, p.ink, 2.2)
  path('M113 175 Q139 213 191 202 Q233 198 248 161 Q242 205 204 213 Q138 228 113 188 Z', p.shade, null)
  path('M160 64 L158 51 Q177 44 193 50 L194 61 Q174 66 160 64 Z', p.color)
  path('M164 49 Q156 25 173 19 Q192 13 196 29 Q199 41 186 49 L180 44 Q193 33 185 28 Q179 23 173 30 Q169 34 174 46 Z', p.gold)
  path('M114 102 Q168 84 238 96 L242 106 Q174 92 110 113 Z', p.color)
  for (let i = 0; i < 11; i++) oval(118 + 11 * i, 106 - 12 * Math.sin(i * Math.PI / 12), 1.7, 1.6, p.ink, null)
  oval(174, 147, 42, 45, p.shade)
  oval(173, 145, 41, 44, p.gold)
  oval(173, 145, 30, 33, p.ink)
  oval(174, 145, 26, 29, p.shade)
  path('M150 137 Q154 119 171 117 L154 145 Z M164 153 L185 121 Q192 124 194 130 L172 158 Z', p.color, null)
  for (let i = 0; i < 10; i++) {
    const angle = i * Math.PI / 5
    oval(173 + 35.5 * Math.cos(angle), 145 + 38 * Math.sin(angle), 1.75, 2, p.ink, null)
  }
  path('M101 132 Q91 128 88 137 L88 159 Q91 168 103 163', p.color)
  path('M240 123 Q253 115 261 119 L264 136 L248 143 Z', p.shade)
  oval(262, 127, 4.5, 9, p.white)
  line('M122 91 Q132 76 153 71 M228 119 Q239 139 235 155 M139 194 Q163 205 185 201', p.white, 2.4)
  at(83, 101, 1, 0, () => {
    for (const [x, y, r] of [[0, 0, 4], [-7, -15, 2], [5, -29, 6], [14, -48, 2.5]]) oval(x, y, r, r, null, p.lead, 1)
  })
}

function winch({ path, line, oval, p }: Brush) {
  oval(99, 145, 83, 4, p.pale, null)
  path('M23 129 L116 118 L150 134 L52 147 Z', p.shade)
  path('M44 130 L57 70 L67 70 L68 130 Z M101 125 L113 68 L121 71 L128 129 Z', p.gold)
  path('M56 75 L106 65 L117 91 L64 104 Z', p.color)
  oval(61, 88, 17, 28, p.shade)
  path('M65 67 L108 61 Q127 84 112 99 L67 108 Q85 92 65 67 Z', p.white)
  for (let i = 0; i < 10; i++) line(`M${66 + i * 4.5} ${68 - i * .64} q16 17 2 37`, p.lead)
  oval(114, 80, 17, 26, p.color)
  oval(115, 80, 5, 8, p.gold)
  path('M115 79 L133 73 L140 51 L147 52 L142 79 L116 85 Z', p.gold)
  line('M74 106 C63 127 91 141 120 134 S162 123 177 137', p.ink, 2)
}

// A folding camera on a focusing rail. The leather bellows are drawn one fold at a time.
function foldingCamera({ path, line, oval, p }: Brush) {
  oval(169, 234, 125, 6, p.pale, null)
  path('M66 190 L235 184 L274 201 L93 216 L59 205 Z', p.color)
  path('M89 211 L125 207 L120 227 L111 231 L103 230 L104 216 L91 218 Z M229 202 L249 199 L251 218 L241 221 L236 208 Z', p.shade)
  path('M65 84 L107 70 L144 80 L141 188 L97 201 L65 189 Z', p.gold, p.ink, 2)
  path('M72 90 L99 97 L98 192 L72 186 Z', p.shade)
  path('M99 89 L140 78 L137 184 L98 196 Z', p.ink)
  path('M102 94 L129 86 L210 114 L209 168 L129 187 L102 180 Z', p.color)
  for (let i = 0; i < 8; i++) {
    const x = 107 + i * 12, y = 92 + i * 2.7, bottom = 181 - i * 1.7
    path(`M${x} ${y} l8 -4 5 8 L${x + 11} ${bottom - 2} l-7 4 -4 -5 Z`, p.shade)
    line(`M${x + 3} ${y + 5} l2 ${bottom - y - 13}`, p.lead, .9)
  }
  path('M197 111 L225 104 L244 111 L244 175 L211 185 L197 177 Z', p.gold)
  path('M209 116 L241 108 L241 173 L209 181 Z', p.color)
  oval(224, 145, 13, 23, p.ink)
  path('M224 124 L244 120 L254 128 L253 154 L239 164 L227 164 Z', p.shade)
  oval(249, 141, 15, 22, p.ink)
  oval(250, 141, 10, 16, p.shade, p.gold)
  path('M244 140 Q245 130 254 130 Q253 136 244 140 Z', p.white, null)
  path('M80 78 L81 62 L107 55 L124 60 L124 71 L117 74 L116 66 L91 68 L90 77 Z', p.shade)
  line('M69 84 L97 91 M68 192 L96 202 M87 205 L244 188 M105 209 L255 196', p.white, 1.3)
  oval(201, 191, 5, 7, p.gold)
}

function filmRoll({ at, path, line, oval, p }: Brush) {
  oval(96, 143, 85, 5, p.pale, null)
  path('M66 93 C101 92 104 123 132 119 Q153 115 160 103 L168 125 C144 154 121 148 99 129 Q82 116 63 123 Z', p.shade)
  line('M78 99 C103 102 115 135 143 124 M77 118 Q95 121 108 133', p.gold, 1.4)
  at(36, 59, .95, -8, () => {
    path('M1 14 Q28 6 56 13 L56 85 Q28 96 1 84 Z', p.gold)
    oval(28, 14, 28, 8, p.shade)
    path('M18 12 L18 0 Q28 -5 38 0 L39 11 Q28 16 18 12 Z', p.ink)
    oval(28, 0, 10, 3, p.color)
    path('M2 33 Q26 42 55 32 L55 64 Q27 74 2 63 Z', p.white)
    line('M8 43 l6 11 7 -9 M24 46 l5 10 8 -11 M41 45 l3 6', p.shade, 2.5)
    line('M4 77 Q28 85 52 76', p.shade)
  })
}

const illustrations = {
  pottery: { main: pottery, companion: potteryTools },
  vinyl: { main: vinyl, companion: sleeves },
  observatory: { main: telescope, companion: starChart },
  postroom: { main: postbox, companion: letters },
  bathysphere: { main: divingBell, companion: winch },
  darkroom: { main: foldingCamera, companion: filmRoll },
}

export type SceneBand = 'composer' | 'sidebar' | 'dialog' | 'empty'

// Each repeat occupies 62 × 20 drawing units. Keep the silhouettes legible at nib size.
function repeatMark(b: Brush, scene: IllustratedScene) {
  const { path, line, oval, p } = b
  switch (scene) {
    case 'pottery':
      line('M0 13 Q8 3 17 12 T36 12 T55 12 L62 11', p.shade, 1.9)
      for (const x of [6, 29, 50]) path(`M${x} 10 q1 -8 7 -7 q1 5 -7 7 Z`, p.color, null)
      line('M2 17 Q29 19 58 16', p.gold)
      break
    case 'vinyl':
      oval(13, 10, 8.5, 8, p.shade, null)
      oval(13, 10, 5.8, 5.4, null, p.pale, .9)
      oval(13, 10, 2, 2, p.gold, null)
      line('M26 11 h4 l2 -4 3 7 3 -12 3 15 3 -8 3 2 h10', p.shade, 1.7)
      break
    case 'observatory':
      line('M0 12 L11 7 L24 13 L42 4 L54 10 L62 9', p.lead, 1.4)
      for (const [x, y] of [[11, 7], [24, 13], [54, 10]]) oval(x, y, 1.9, 1.9, p.shade, null)
      star(b, 42, 5, .7)
      break
    case 'postroom':
      path('M3 5 L23 2 L25 15 L5 18 Z', p.white, p.shade, 1.4)
      line('M4 6 l10 5 9 -8 M5 17 l6 -7 M23 14 l-6 -5', p.shade)
      for (const y of [5, 10, 15]) line(`M31 ${y} q5 -4 12 -1 t16 -1`, p.color, 1.8)
      break
    case 'bathysphere':
      line('M0 14 Q5 16 10 8 Q14 2 18 7 Q14 7 15 11 Q21 17 30 10 Q37 4 43 11 Q48 17 61 10', p.shade, 1.9)
      line('M2 18 q6 1 10 -2 M22 17 q6 0 9 -3', p.color)
      oval(46, 4, 2.1, 2.1, null, p.lead)
      oval(52, 1.5, 1, 1, p.gold, null)
      break
    case 'darkroom':
      path('M0 2 L62 2 L62 18 L0 18 Z', p.shade, null)
      for (const x of [3, 23, 43]) {
        path(`M${x} 5 h16 v10 h-16 Z`, p.pale, null)
        path(`M${x + 1} 14 l5 -5 3 2 3 -4 3 7 Z`, p.color, null)
      }
      for (let x = 2; x < 62; x += 7) {
        line(`M${x} 3.4 h2 M${x} 16.6 h2`, p.white, 1)
      }
      break
  }
}

export function paintSceneBand(ctx: CanvasRenderingContext2D, scene: IllustratedScene, dark: boolean, w: number, h: number, band: SceneBand) {
  const p = palettes[scene][dark ? 'dark' : 'light']
  const b = brush(ctx, p)
  ctx.lineCap = 'round'
  ctx.lineJoin = 'round'
  if (band === 'empty') {
    const scale = Math.min(h / 38, w / 200)
    for (const direction of [-1, 1]) {
      const x = w / 2 + direction * 66 * scale
      b.at(x - 31 * scale, h / 2 - 10 * scale, scale, direction * 5, () => repeatMark(b, scene))
    }
    return
  }
  if (band === 'sidebar') {
    paintSidebarOrnament(b, scene, w, h, illustrations[scene].companion)
    return
  }
  if (band === 'composer') {
    paintComposerOrnament(b, scene, w, h)
    return
  }

  const scale = Math.min(h / 23, 1)
  const step = 62 * scale
  const count = Math.ceil(w / step)
  const start = (w - count * step) / 2
  for (let i = 0; i < count; i++) {
    b.at(start + i * step, h / 2 - 10 * scale, scale, 0, () => repeatMark(b, scene))
  }
}

export function paintHomeScene(ctx: CanvasRenderingContext2D, scene: IllustratedScene, dark: boolean, w: number, h: number, preview: boolean) {
  const p = palettes[scene][dark ? 'dark' : 'light']
  const b = brush(ctx, p)
  ctx.lineCap = 'round'
  ctx.lineJoin = 'round'
  ctx.fillStyle = p.paper
  ctx.fillRect(0, 0, w, h)
  if (scene === 'observatory' || scene === 'postroom' || scene === 'bathysphere') {
    paintPanorama(ctx, b, scene, w, h, dark, illustrations[scene].main)
  } else {
    paintStillLife(ctx, b, scene, w, h, dark, preview)
  }

  // Paper grain is deterministic so the ink stays still when a window resizes.
  const noise = seededRandom(71)
  ctx.fillStyle = p.ink
  ctx.globalAlpha = dark ? .12 : .095
  for (let i = 0; i < Math.min(12000, w * h / 70); i++) {
    const x = noise() * w, y = noise() * h
    ctx.fillRect(x, y, .4 + noise() * 1.3, .6)
  }
  ctx.globalAlpha = 1
}

function paintStillLife(ctx: CanvasRenderingContext2D, b: Brush, scene: IllustratedScene, w: number, h: number, dark: boolean, preview: boolean) {
  const p = b.p
  const mobile = !preview && w < 640
  const scale = preview ? Math.min(w / 400, h / 250) : mobile ? Math.min(w / 575, h / 1000) : Math.min(w / 975, h / 700, 1.35)
  const { main, companion } = illustrations[scene]

  // A shallow strip of paper and pencil marks ties the two corner vignettes together.
  const floor = preview ? .87 : mobile ? .27 : .90
  const floorY = h * floor
  b.path(`M0 ${floorY} Q${w * .18} ${floorY - 9 * scale} ${w * .34} ${floorY + 9 * scale}
    T${w * .74} ${floorY + 3 * scale} Q${w * .9} ${floorY - 12 * scale} ${w} ${floorY - 6 * scale}
    L${w} ${mobile ? floorY + 8 * scale : h} L0 ${mobile ? floorY + 11 * scale : h} Z`, p.pale, null)
  const mainScale = preview ? scale * .95 : scale
  const smallScale = preview ? scale * .83 : scale * .9
  b.at(w - 290 * mainScale, floorY - 228 * mainScale, mainScale, 0, () => main(b))
  b.at(-12 * smallScale, floorY - 136 * smallScale, smallScale, 0, () => companion(b))

  // A small printer's ornament echoes the binding on the navigation and composer.
  if (!preview && !mobile && w >= 720 && h >= 540) {
    ctx.globalAlpha = dark ? .62 : .72
    b.at(w - 174 * scale, 62 * scale, scale * .86, -7, () => {
      repeatMark(b, scene)
      b.line('M-23 11 L-8 10 M68 9 L83 8', p.lead)
    })
    ctx.globalAlpha = 1
  }
}
