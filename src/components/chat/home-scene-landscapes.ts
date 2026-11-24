import { seededRandom, type Brush } from './scene-brush'

type Panorama = 'observatory' | 'postroom' | 'bathysphere'
type Landscape = {
  ctx: CanvasRenderingContext2D
  b: Brush
  w: number
  h: number
  dark: boolean
  portrait: boolean
  subject: (brush: Brush) => void
}

// Sky, water and terrain follow the viewport. Buildings, animals and instruments
// have their own uniform scale, so the portrait composition keeps their proportions.
function field({ ctx, w, h }: Landscape, draw: () => void) {
  ctx.save()
  ctx.scale(w / 1000, h / 700)
  draw()
  ctx.restore()
}

function wash({ ctx, w, h }: Landscape, colors: [number, string][]) {
  const gradient = ctx.createLinearGradient(0, 0, 0, h)
  for (const [stop, color] of colors) gradient.addColorStop(stop, color)
  ctx.fillStyle = gradient
  ctx.fillRect(0, 0, w, h)
}

function summitStation({ path, line, oval, p }: Brush) {
  // An offset copper shutter follows the curve of the dome. The long hall
  // and its illuminated slit windows sit on the same stone retaining wall.
  path('M-83 5 L-77 -28 L-1 -37 L49 -10 L51 10 Z', p.pale, p.ink, 1.8)
  path('M-88 -27 L-58 -51 L-3 -56 L15 -37 L-18 -34 Z', p.shade)
  line('M-77 -31 L-53 -46 L-10 -50 M-71 -30 L-48 -45 M-60 -32 L-39 -46', p.color, 1.2)
  path('M-24 -60 Q-7 -37 61 -49 L62 -3 Q23 15 -24 4 Z', p.color)
  path('M-22 -63 C-19 -108 12 -130 40 -116 Q67 -106 69 -64 Q44 -45 -22 -54 Z', p.pale, p.ink, 1.8)
  path('M19 -117 Q2 -92 4 -55 L14 -55 Q12 -94 28 -117 Z', p.shade)
  line('M-10 -65 Q-7 -98 9 -108 M34 -114 Q53 -101 57 -67 M41 -112 Q61 -98 64 -67', p.lead, 1.3)
  path('M-26 -62 Q18 -52 70 -66 L69 -57 Q26 -42 -25 -53 Z', p.gold)
  line('M-19 -48 Q23 -38 64 -51 M-22 -2 Q21 7 58 -6', p.shade, 1.7)
  for (const x of [-69, -57, -45, -6, 8, 38, 51]) {
    const y = x < -24 ? -20 : -31
    path(`M${x} ${y} l6 -1 v12 l-6 1 Z`, p.ink, null)
    line(`M${x + 2} ${y + 2} v7`, p.gold, 1.8)
  }
  oval(24, -37, 2.2, 1.8, p.gold, null)
  path('M-92 5 L-55 2 Q-15 13 62 -3 L72 2 L69 8 Q8 23 -89 13 Z', p.shade)
}

function stargazing(s: Landscape) {
  const { b, w, h, dark, portrait, subject } = s
  const { path, line, oval, at } = b
  const far = dark ? '#544b70' : '#ada3c3'
  const ridge = dark ? '#3b3959' : '#857e9f'
  const near = dark ? '#29283e' : '#625d7c'
  wash(s, [[0, dark ? '#20233f' : '#aaa4cc'], [.37, dark ? '#39334e' : '#e2d8e9'],
  [.65, dark ? '#554155' : '#f9dfcc'], [1, dark ? '#2b293f' : '#b1a1b2']])
  field(s, () => {
    path('M-50 164 C114 117 141 78 318 51 C412 35 452 5 502 -17 L626 -17 C526 42 458 58 345 74 C185 98 122 146 -50 183 Z', dark ? '#44405e' : '#c1b8d8', null)
    line('M-10 169 C103 140 161 84 308 62 M351 55 Q431 43 488 13 M38 169 Q92 154 136 126', dark ? '#645671' : '#d2c6dd', 1.25)
    path('M0 529 L63 501 L114 511 L194 474 L239 490 L290 465 L331 477 L409 438 L467 459 L555 413 L615 451 L682 438 L749 466 L816 439 L880 455 L946 413 L1000 429 V700 H0 Z', far, null)
    line('M339 476 L407 440 L395 459 L414 452 L428 458 M484 454 L555 414 L537 437 L551 432 L573 445 M897 448 L945 415 L936 432', dark ? '#89718b' : '#f0d9d2', 2)
    path('M0 495 Q65 489 129 523 T259 535 Q328 512 429 539 T662 523 L753 492 L789 505 L844 477 L890 491 L966 452 L1000 472 V700 H0 Z', ridge, null)
    path('M0 560 Q117 525 220 558 T440 593 Q562 580 641 595 T852 576 Q929 558 1000 563 V700 H0 Z', near, null)
    path('M0 587 Q102 553 196 573 Q245 584 282 587 Q222 595 115 580 Q73 575 0 601 Z', dark ? '#49415a' : '#aa94a3', null)
    line('M-10 612 Q92 578 156 590 T287 603 M349 609 Q413 613 451 609 M741 607 Q800 603 845 588', dark ? '#63516a' : '#aa95aa', 1.6)
    // The switchback begins at the dome and threads through the cut banks.
    path('M137 566 C188 578 312 576 303 595 C298 610 195 613 211 626 C239 649 523 600 564 643 C578 660 543 683 512 700 H466 C533 666 549 653 530 644 C476 621 263 659 206 635 C158 612 282 601 286 592 Q289 583 136 569 Z', dark ? '#6c5769' : '#c8aaaf', null)
    path('M0 650 L59 625 L91 638 L119 622 L151 645 L212 659 L248 679 L310 686 L342 700 H0 Z M1000 601 L942 604 L912 626 L854 641 L831 658 L775 674 L708 700 H1000 Z', dark ? '#202236' : '#4e4e69', null)
    line('M19 653 L60 634 L76 641 M107 640 l13 -9 22 17 M864 645 l45 -12 29 -20 M754 689 l64 -21 m8 -3 19 -5', dark ? '#52465b' : '#908398', 1.6)
    const scree = seededRandom(108)
    for (let i = 0; i < 58; i++) {
      const x = scree() * 1000, y = 609 + scree() * 95, n = 2 + scree() * 6
      line(`M${x} ${y} l${n} -1.4`, dark ? '#52475b' : '#8f8094', 1)
    }
  })
  const random = seededRandom(1912)
  const starlight = dark ? '#d8cbdf' : '#fff2e5'
  for (let n = 0; n < (portrait ? 100 : 175); n++) {
    const x = random() * w, y = random() * h * .56, r = .45 + random() ** 3 * 1.65
    oval(x, y, r, r, starlight, null)
  }
  const mx = w * (portrait ? .74 : .80), my = h * .135
  at(mx, my, portrait ? .77 : 1, -15, () => {
    path('M-5 -36 C-47 -29 -48 25 -13 36 Q9 45 27 27 C-11 36 -33 -6 -5 -36 Z', dark ? '#f8d49a' : '#fff2ca', null)
    line('M-23 -23 Q-43 10 -14 29 M-27 0 q-1 10 5 16', dark ? '#ca9b87' : '#d8b7a0', 1.1)
  })
  at(w * .12, h * .16, portrait ? .66 : 1, -10, () => {
    line('M-48 20 L-22 -7 L3 1 L26 -17 L59 -9', dark ? '#a193b8' : '#8d83ab', 1)
    for (const [x, y, r] of [[-48, 20, 2], [-22, -7, 3.8], [3, 1, 2.2], [26, -17, 4.8], [59, -9, 1.7]]) {
      if (r > 3) path(`M${x} ${y - r * 1.7} q1 ${r * 1.5} ${r} ${r * 1.7} q-${r} 1 -${r} ${r * 1.7} q-1 -${r * 1.5} -${r} -${r * 1.7} q${r} -1 ${r} -${r * 1.7} Z`, starlight, null)
      else oval(x, y, r, r, starlight, null)
    }
  })
  at(w * .14, h * (portrait ? .793 : .79), portrait ? .79 : 1.17, 0, () => summitStation(b))
  oval(w * .88, h * .82 + 3, 54, 3.3, dark ? '#29273e' : '#716a86', null)
  at(w * .88 - 160 * .59, h * .82 - 231 * .59, .59, 0, () => subject(b))
  // A stand of wind-shaped pines closes the outer corners of the plate.
  for (const [x, y, size] of [[.018, .84, .95], [.045, .85, .64], [.09, .88, .52], [.96, .83, .77], [.985, .84, 1.15]]) {
    at(w * x, h * y, size, 0, () => {
      path('M-2 6 L-1 -52 L-19 -39 L-11 -54 L-17 -51 L-6 -70 L-12 -67 L0 -93 L8 -72 L4 -73 L13 -58 L8 -60 L18 -44 L4 -50 L3 7 Z', dark ? '#202436' : '#514e6a', null)
    })
  }
}

function biplane({ path, line, oval, p }: Brush) {
  path('M-41 -4 L-70 -21 Q-74 -22 -73 -15 L-68 1 L-47 10 Z', p.shade)
  path('M-64 3 Q-21 -5 32 -7 Q53 -9 65 -1 L61 10 Q40 25 3 18 L-49 11 Z', p.color, p.ink, 1.8)
  path('M-43 9 Q2 18 52 9 Q40 22 5 20 Z', p.gold, null)
  path('M7 -6 Q13 -19 27 -13 L33 -7 Z', p.ink)
  line('M12 -9 q6 -6 13 -3', p.white)
  path('M-8 12 L27 12 L-7 31 L-40 31 Z', p.gold)
  line('M-23 21 L-29 -13 L-1 12 L-5 -17 M16 14 L8 -16', p.ink, 1.5)
  path('M-53 -14 Q-58 -17 -48 -20 L32 -32 Q43 -35 53 -29 L52 -26 L-32 -11 Z', p.color, p.ink, 1.6)
  line('M-37 -17 L37 -29 M-54 1 L-62 -10', p.white, 1.4)
  path('M54 -7 L64 -8 L68 6 L58 12 Z', p.pale)
  line('M68 -19 L69 22', p.ink, 2)
  oval(68, -10, 2.5, 10, p.gold)
  oval(69, 15, 2, 8, p.gold)
  line('M17 17 L22 28 L36 16', p.ink, 2)
  oval(23, 28, 5.5, 5.5, p.ink)
  oval(23, 28, 2.3, 2.3, p.gold, null)
  path('M-32 1 l12 1 -1 7 -12 -1 Z', p.white, null)
  line('M-32 2 l6 4 6 -3', p.shade, .9)
}

function townhouse(b: Brush, walls: string, roof: string, lit: string, gable: boolean) {
  const { path, line, p } = b
  path('M-29 -67 L19 -65 L30 -57 L29 2 L-28 2 Z', walls, p.ink, 1.15)
  path('M19 -65 L30 -58 L29 2 L18 2 Z', p.shade, null)
  path(gable ? 'M-33 -64 L-7 -92 L23 -68 L31 -61 L18 -63 L-6 -82 L-24 -62 Z' : 'M-36 -65 L-25 -82 L17 -79 L31 -60 L18 -64 Z', roof, p.ink, 1.4)
  line('M-25 -59 L13 -57 M-26 -28 L14 -27 M21 -54 v45', p.lead, 1.1)
  for (const x of [-21, -8, 5]) {
    for (const y of [-51, -22]) {
      path(`M${x} ${y} q3 -4 6 0 v11 h-6 Z`, lit, p.shade, .9)
      line(`M${x} ${y + 4} h6`, p.shade, .7)
    }
  }
  path('M-9 -1 v-13 q3 -9 8 -3 v16 Z', p.shade)
}

function afternoonMail(s: Landscape) {
  const { b, w, h, dark, portrait, subject, ctx } = s
  const { path, line, oval, at, p } = b
  wash(s, [[0, dark ? '#453047' : '#f3b6a8'], [.43, dark ? '#423043' : '#ffe8cf'],
  [.76, dark ? '#70505a' : '#f2c1a6'], [1, dark ? '#473446' : '#d6a393']])
  field(s, () => {
    const cloud = dark ? '#694a5b' : '#fff0d6'
    path('M-20 140 Q8 130 42 136 Q55 119 70 123 Q86 96 110 113 Q116 94 136 97 Q159 98 167 121 Q181 118 192 129 Q216 125 239 134 Q178 146 100 143 Q18 141 -20 154 Z M690 84 Q721 67 749 72 Q766 49 789 65 Q817 30 845 59 Q859 53 868 64 Q890 62 907 78 Q814 87 690 84 Z', cloud, null)
    line('M4 158 Q53 151 116 154 M146 150 l37 -2 M749 94 Q809 90 837 92', dark ? '#966b74' : '#e2ad9b', 1.4)
    path('M0 470 Q127 398 238 460 Q327 477 414 444 Q520 397 637 459 Q753 481 836 448 Q917 409 1000 432 V700 H0 Z', dark ? '#795663' : '#d4a6a8', null)
    path('M0 490 Q123 438 231 480 Q321 504 474 470 T721 487 Q845 514 1000 454 V700 H0 Z', dark ? '#604553' : '#be9096', null)
    path('M0 540 Q165 513 347 539 T679 533 Q877 504 1000 515 V700 H0 Z', dark ? '#443a4c' : '#cfb2b0', null)
    for (let i = 0; i < 16; i++) {
      const x = (i * 137) % 990, y = 564 + (i * 31) % 126
      line(`M${x} ${y} q26 -3 52 -1 m7 1 h15`, dark ? '#80636e' : '#f6d6be', 1.5)
    }
  })
  // The flight path loops around the plane, high above the writing area.
  ctx.save()
  ctx.setLineDash([3.5, 6])
  line(`M-8 ${h * .32} C${w * .13} ${h * .22} ${w * .04} ${h * .12} ${w * .025} ${h * .18} S${w * .10} ${h * .25} ${w * .22} ${h * .15}`, dark ? '#bd8793' : '#b67375', 1.35)
  ctx.restore()
  at(w * .23, h * .15, portrait ? .69 : 1.0, -11, () => biplane(b))
  // Small roofs sit behind the quay, with a clock gable over the sorting hall.
  const base = h * (portrait ? .757 : .787)
  const size = portrait ? .68 : 1.08
  at(w * (portrait ? .83 : .865), base, size, 0, () => {
    for (const [x, y, k] of [[-144, -14, .61], [-110, -13, .74], [85, -8, .88], [126, -7, .67]]) {
      at(x, y, k, 0, () => townhouse(b, p.pale, p.shade, p.gold, false))
    }
    at(-66, -2, .9, 0, () => townhouse(b, p.gold, p.color, p.white, true))
    at(53, 1, .94, 0, () => townhouse(b, p.pale, p.shade, p.white, false))
    path('M-37 2 V-89 L9 -113 L41 -84 L40 3 Z', p.color)
    path('M-43 -86 L5 -126 L49 -85 L42 -79 L6 -113 L-36 -80 Z', p.shade)
    line('M-29 -82 L6 -111 L34 -88 M-32 -50 h68 M-32 -44 h68', p.white, 2)
    oval(5, -81, 12.5, 12.5, p.gold)
    oval(5, -81, 9.3, 9.3, p.white, p.shade)
    line('M5 -87 v6 l5 2', p.ink, 1.5)
    for (const x of [-24, -3, 18]) {
      path(`M${x} -1 v-27 q6 -15 13 -2 v29 Z`, p.ink)
      line(`M${x + 4} -5 v-20 m5 -4 v22`, p.gold, 1.4)
    }
    path('M-169 8 L182 3 L194 22 Q146 26 78 20 T-65 25 L-113 22 L-110 17 L-150 19 Z', dark ? '#77616c' : '#b89995', null)
    path('M-172 3 L187 1 L192 7 L-170 12 Z', p.pale)
    line('M-104 13 l35 -1 m6 0 13 -1 M5 16 l36 -1 M101 13 l22 -1 M134 10 l3 8', p.shade, 1.1)
  })
  field(s, () => {
    const bridge = dark ? '#674756' : '#ba8583'
    path('M-8 512 Q47 497 87 501 Q158 507 244 532 L265 541 Q203 552 178 545 Q176 523 152 523 Q133 522 131 543 L110 543 Q107 520 82 518 Q60 519 56 544 L36 546 Q35 529 18 529 Q2 529 -8 550 Z', bridge, p.shade, 1.2)
    line('M-4 511 Q68 484 158 512 L250 535', p.white, 2.2)
    path('M0 602 Q67 599 127 612 L266 650 L315 664 L302 675 L276 677 L265 686 L238 687 L221 700 H0 Z', dark ? '#5d414d' : '#bd8e85', null)
    path('M0 601 Q71 596 129 611 L309 661 L303 665 L246 653 Q122 622 0 616 Z', dark ? '#9a6f70' : '#f0c5a6', null)
    line('M10 624 l31 1 m9 0 29 3 M98 633 l26 4 M231 665 l44 10 M210 686 l22 4', p.shade, 1.25)
  })
  at(w * .095 - 168 * .55, h * .871 - 235 * .55, .55, 0, () => subject(b))
  at(w * .56, h * .91, portrait ? .62 : .93, -3, () => {
    line('M-77 18 Q-29 14 32 21 M-50 24 l36 -1 M35 19 l29 -1', dark ? '#a17c80' : '#f2d3bc', 1.35)
    path('M-63 -3 Q1 5 62 -7 Q56 4 37 12 Q-12 18 -49 11 Z', p.shade)
    line('M-57 0 Q-6 9 54 -3', p.white, 1.4)
    path('M-31 -1 l3 -17 24 -2 6 20 Z', p.color)
    path('M-34 -17 l5 -5 25 -1 5 5 Z', p.gold)
    path('M-24 -14 l7 -1 v8 l-8 1 Z M-13 -15 l7 -1 2 8 -8 1 Z', p.pale, null)
    path('M9 1 l0 -10 14 -2 2 10 Z M26 -1 l1 -7 12 -1 2 6 Z', p.gold)
    line('M17 -10 v8 M32 -8 v5', p.shade)
  })
}

function seaFan(b: Brush, color: string, highlights: string) {
  const { line } = b
  line('M0 0 Q-6 -23 -5 -47 Q-6 -70 -15 -86 M-4 -26 Q-24 -32 -35 -57 M-6 -46 Q14 -58 17 -82 M-6 -56 Q-25 -68 -29 -90 M3 -53 Q23 -60 36 -77', color, 4.2)
  line('M-13 -32 Q-28 -38 -39 -37 M-24 -43 q-3 -12 -12 -18 M-32 -51 l-12 -4 M-10 -67 q1 -14 -4 -27 M-24 -77 l-14 -7 M8 -62 l1 -20 M15 -74 l13 -17 M22 -63 l16 -2 M30 -71 l4 -15', color, 2.4)
  line('M-1 -4 Q-6 -37 -5 -48 M-8 -54 l-12 -11 M10 -63 l4 -9', highlights, 1)
}

function descent(s: Landscape) {
  const { b, w, h, dark, portrait, subject, ctx } = s
  const { path, line, oval, at } = b
  wash(s, [[0, dark ? '#25465c' : '#82bed9'], [.32, dark ? '#253c52' : '#c6e7ed'],
  [.58, dark ? '#20344a' : '#d5e8e9'], [.82, dark ? '#203449' : '#91bfce'], [1, dark ? '#14293c' : '#6292a9']])
  field(s, () => {
    const light = ctx.createLinearGradient(0, 0, 0, 475)
    light.addColorStop(0, dark ? '#86b1c13d' : '#fffbdd63')
    light.addColorStop(1, '#eff8ff00')
    for (const [x, width, slant] of [[15, 64, 95], [103, 17, 135], [234, 89, 152], [424, 35, 175], [736, 72, 177]]) {
      ctx.fillStyle = light
      ctx.fill(new Path2D(`M${x} 0 h${width} L${x + width + slant + 93} 475 L${x + slant} 475 Z`))
    }
    line('M-12 13 Q24 24 67 12 T163 12 M211 14 Q258 3 287 9 M589 14 Q642 5 683 13 T799 12 M864 6 Q912 17 987 8', dark ? '#688697' : '#e9f3e9', 1.7)
    path('M0 414 Q81 410 112 467 Q134 504 194 496 Q272 494 331 543 Q417 593 541 562 T761 531 Q847 498 897 465 Q963 423 1000 438 V700 H0 Z', dark ? '#304a60' : '#9cbfcf', null)
    path('M0 515 L42 497 L75 508 L98 541 L124 555 L171 553 L226 577 L242 593 Q340 643 473 608 T707 596 L782 560 L831 555 L894 502 L939 514 L1000 470 V700 H0 Z', dark ? '#263e54' : '#739eb5', null)
    path('M0 544 L40 531 L85 558 L102 583 L165 600 L189 631 Q281 664 372 647 Q506 622 600 646 T806 620 L840 595 L889 588 L942 551 L1000 541 V700 H0 Z', dark ? '#182e43' : '#527f9a', null)
    line('M-12 552 L38 537 L75 558 M100 586 l28 12 20 -1 M802 630 l44 -31 32 -5 M922 574 l25 -18 47 -8', dark ? '#4d6679' : '#a8c2c9', 1.9)
  })
  const size = portrait ? .56 : .88
  const bellX = w * (portrait ? .78 : .86), bellY = h * (portrait ? .13 : .19)
  line(`M${bellX - 28} -5 Q${bellX - 24} ${bellY * .48} ${bellX} ${bellY + 3}`, dark ? '#a2a2a0' : '#607d91', 2.6)
  line(`M${bellX - 25} -5 Q${bellX - 21} ${bellY * .48} ${bellX + 2} ${bellY + 3}`, dark ? '#566674' : '#cfccc0', .9)
  at(bellX - 177 * size, bellY - 24 * size, size, 0, () => subject(b))
  const random = seededRandom(816)
  for (let i = 0; i < (portrait ? 36 : 62); i++) {
    const x = random() * w, y = random() * h * .82, r = .65 + random() ** 3 * 3.7
    oval(x, y, r, r, null, dark ? '#5c788d' : '#a6c8ce', .85)
  }
  at(w * .13, h * .34, portrait ? .59 : .90, -8, () => {
    for (const [x, y, k] of [[-72, 14, .89], [-38, -3, .68], [-9, 11, 1], [25, -17, .78], [47, 2, .65], [74, -14, .54], [-52, 37, .6], [10, 38, .49]]) {
      at(x, y, k, 0, () => {
        path('M-12 -1 Q-5 -8 7 -4 L17 0 Q5 8 -4 6 L-12 2 L-19 7 L-18 -7 Z', dark ? '#83a4b4' : '#618ba2', null)
        line('M-5 -1 q6 -2 12 0', dark ? '#b6c7c6' : '#bddee2', 1.3)
      })
    }
  })
  const coral = dark ? '#ae777e' : '#d99282', ribs = dark ? '#d59c91' : '#f5c6a4'
  for (const [x, y, k, r] of [[.04, .88, 1.1, -14], [.09, .90, .74, 20], [.91, .90, 1.06, 15], [.968, .86, .77, -12]]) {
    at(w * x, h * y, k, r, () => seaFan(b, coral, ribs))
  }
  at(w * .15, h * .92, .85, -7, () => {
    for (const [x, y, k] of [[0, 0, 1], [17, 3, .7], [-14, -1, .66]]) {
      at(x, y, k, 0, () => {
        path('M-6 0 Q-9 -15 -10 -27 Q-7 -33 -1 -28 L4 -3 Z', dark ? '#8a8396' : '#c7a2ad', b.p.shade)
        oval(-6, -28, 4.4, 2.3, b.p.shade)
      })
    }
  })
  field(s, () => {
    const rock = dark ? '#17283b' : '#48718c'
    path('M-8 606 L20 601 L44 612 L69 627 L94 622 L111 631 L122 646 L156 644 L173 652 L184 669 L224 676 L246 695 L257 700 H0 Z M1000 591 L964 595 L950 605 L932 607 L918 626 L895 628 L877 642 L858 642 L828 671 L779 684 L757 700 H1000 Z', rock, null)
    line('M2 608 l18 -3 19 9 M60 630 l34 -4 10 5 M127 650 l23 -2 15 8 M899 632 l21 -3 14 -17 M834 670 l23 -22 13 -1 M943 618 l14 -13', dark ? '#4d6271' : '#95aeb7', 1.7)
    line('M209 686 Q268 675 309 683 T408 681 M329 670 q41 4 82 -6 M477 686 q34 -6 78 -2 M645 673 q44 5 76 -6', dark ? '#314758' : '#799eb1', 1.2)
  })
  at(w * .92, h * .946, portrait ? .72 : .94, -10, () => {
    const shell = dark ? '#b39297' : '#ddb1a5'
    path('M-30 7 Q-32 -13 -13 -28 Q7 -41 22 -27 Q36 -14 27 1 Q22 14 7 14 L-1 18 L-10 14 Z', shell, b.p.ink, 1.3)
    line('M-25 4 Q-21 -19 1 -25 Q21 -30 23 -13 Q26 0 11 7 Q-1 12 -4 1 Q-5 -8 3 -11 Q14 -15 16 -6 Q16 0 9 1 Q5 1 5 -3', b.p.shade, 1.5)
    line('M-23 -12 l6 3 M-17 -21 l7 5 M-8 -27 l5 6 M2 -31 l2 5 M14 -31 l-2 6 M22 -27 l-3 4', b.p.shade, 1)
  })
}

export function paintPanorama(ctx: CanvasRenderingContext2D, b: Brush, scene: Panorama, w: number, h: number, dark: boolean, subject: (brush: Brush) => void) {
  const portrait = w / h < .85
  const scale = Math.min(w / (portrait ? 490 : 1000), h / (portrait ? 760 : 680))
  ctx.save()
  ctx.scale(scale, scale)
  const landscape = { ctx, b, w: w / scale, h: h / scale, dark, portrait, subject }
  const paintings = { observatory: stargazing, postroom: afternoonMail, bathysphere: descent }
  paintings[scene](landscape)
  ctx.restore()
}
