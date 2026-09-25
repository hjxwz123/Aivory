export type ArtworkVariant = 'mercury' | 'eclipse' | 'aurora'

/**
 * Art direction: one material/light study per visit, floating in open space.
 * Mercury is sculptural, eclipse is optical, aurora is fluid. Theme tokens
 * color every material, including the monochrome edition. No bitmap assets.
 * The DOM reserves space for the subject; light can travel beyond that space.
 */
export const artworkVertex = `#version 300 es
in vec2 position;
void main() { gl_Position = vec4(position, 0.0, 1.0); }
`

const common = `#version 300 es
precision highp float;
uniform vec2 uResolution;
uniform vec2 uViewport;
uniform vec2 uFocus;
uniform vec2 uPointer;
uniform float uScale;
uniform float uTime;
uniform float uDark;
uniform float uCompact;
uniform vec3 uAccent;
uniform vec3 uTint;
uniform vec3 uPearl;
uniform vec4 uQuiet[4];
out vec4 fragColor;

const float TAU = 6.28318530718;
mat2 rotate(float angle) {
  float c = cos(angle), s = sin(angle);
  return mat2(c, -s, s, c);
}
float hash(vec2 p) {
  vec3 p3 = fract(vec3(p.xyx) * .1031);
  p3 += dot(p3, p3.yzx + 33.33);
  return fract((p3.x + p3.y) * p3.z);
}
float noise(vec2 p) {
  vec2 i = floor(p), f = fract(p);
  f = f * f * (3.0 - 2.0 * f);
  return mix(mix(hash(i), hash(i + vec2(1, 0)), f.x),
             mix(hash(i + vec2(0, 1)), hash(i + vec2(1, 1)), f.x), f.y);
}
float field(vec2 p) {
  return noise(p) * .57 + noise(p * 2.03 + 7.1) * .28 + noise(p * 4.11 + 1.7) * .15;
}
float sq(float x) { return x * x; }
// Integrate subpixel filaments instead of letting them shimmer on small screens.
float filament(float phase, float sharpness) {
  float detail = pow(.5 + .5 * sin(phase), sharpness);
  return mix(detail, .22, smoothstep(.8, 3.0, fwidth(phase)));
}
vec4 over(vec4 front, vec4 back) { return front + back * (1.0 - front.a); }
vec4 light(float energy, float tint) {
  float alpha = 1.0 - exp(-max(energy, 0.0));
  vec3 color = mix(uAccent, uTint, clamp(tint, 0.0, 1.0));
  color = mix(color, uPearl, smoothstep(.65, 3.8, energy) * mix(.48, .88, uDark));
  return vec4(color * alpha, alpha);
}
float distanceToBox(vec2 p, vec4 box) {
  vec2 d = abs(p - box.xy) - box.zw;
  return length(max(d, 0.0)) + min(max(d.x, d.y), 0.0);
}
// Real content bounds, with a broad falloff instead of a rectangular cut.
float quietSpace(vec2 p) {
  float visibility = 1.0;
  for (int i = 0; i < 4; i++) {
    visibility *= smoothstep(0.0, mix(58.0, 22.0, uCompact), distanceToBox(p, uQuiet[i]));
  }
  return visibility;
}

vec4 atmosphere(vec2 q, float t) {
  vec2 drift = vec2(sin(t * .13), cos(t * .17)) * .16;
  vec2 p = rotate(-.35) * (q - drift);
  float cloud = exp(-dot(p * vec2(.43, .62), p * vec2(.43, .62)));
  float beam = exp(-sq((p.y + .18 * sin(p.x * 1.3 - t * .24)) * 2.2));
  float energy = cloud * (.07 + .13 * beam) * mix(.72, 1.0, uDark);
  vec4 mist = light(energy * mix(1.0, .35, uCompact), .45 + .3 * sin(t * .12));

  // Sparse motes move in depth. Each grid cell owns one smooth, tiny sprite.
  vec2 stars = rotate(t * .008) * q * 6.0 + vec2(t * .022, -t * .034);
  vec2 cell = floor(stars);
  vec2 offset = vec2(hash(cell + 5.2), hash(cell + 17.8)) * .64 + .18;
  vec2 delta = fract(stars) - offset;
  float rare = step(.91, hash(cell + 53.1));
  float radius = mix(.016, .034, hash(cell + 29.3));
  float mote = exp(-dot(delta, delta) / (radius * radius));
  mote += exp(-dot(delta, delta) / (radius * radius * 15.0)) * .10;
  mote *= rare * (.6 + .4 * sq(sin(t * .6 + hash(cell) * TAU)));
  mote *= exp(-dot(q, q) * .15) * smoothstep(.6, 1.3, length(q));
  return over(light(mote * .85 * (1.0 - uCompact), .6), mist);
}
`

const mercury = `
vec3 sculpt(vec3 p, float t) {
  p.xz = rotate(.28 * sin(t * .18) + uPointer.x * .16) * p.xz;
  p.yz = rotate(.56 + .28 * sin(t * .21) + uPointer.y * .12) * p.yz;
  p.xy = rotate(-.45 + t * .10) * p.xy;
  return p;
}
float sculpture(vec3 point, float t) {
  vec3 p = sculpt(point, t);
  float angle = atan(p.y, p.x);
  float radius = .77 + .075 * sin(angle * 3.0 + t * .43);
  vec2 tube = vec2(length(p.xy) - radius, p.z - .12 * sin(angle * 2.0 - t * .3));
  tube = rotate(angle * .5 + t * .13) * tube;
  tube.y *= 1.18;
  float thickness = .235 + .065 * sin(angle * 3.0 - t * .51);
  float body = (length(tube) - thickness) * .68;
  vec3 satellite = vec3(cos(t * .35), sin(t * .35), .16 * sin(t * .5)) * 1.24;
  float droplets = min(length(p - satellite) - .076, length(p + satellite * .94) - .045);
  return min(body, droplets);
}
vec3 sculptureNormal(vec3 p, float t) {
  vec2 e = vec2(.0015, -.0015);
  return normalize(e.xyy * sculpture(p + e.xyy, t) + e.yyx * sculpture(p + e.yyx, t)
                 + e.yxy * sculpture(p + e.yxy, t) + e.xxx * sculpture(p + e.xxx, t));
}
vec4 scene(vec2 q, float t) {
  q -= vec2(sin(t * .27) * .026, cos(t * .32) * .035);
  vec4 halo = light(exp(-sq((length(q) - .91) * 3.1)) * .16, .65);
  vec3 origin = vec3(0, 0, 4.2);
  vec3 ray = normalize(vec3(q, -3.8));
  float b = dot(origin, ray);
  float discriminant = b * b - dot(origin, origin) + 1.46 * 1.46;
  if (discriminant < 0.0) return halo;
  float travel = -b - sqrt(discriminant);
  float end = -b + sqrt(discriminant);
  vec3 p = origin + ray * travel;
  bool hit = false;
  for (int i = 0; i < 70; i++) {
    p = origin + ray * travel;
    float d = sculpture(p, t);
    if (d < .0016) { hit = true; break; }
    travel += d;
    if (travel > end) break;
  }
  if (!hit) return halo;

  vec3 normal = sculptureNormal(p, t);
  vec3 reflection = reflect(ray, normal);
  float facing = max(dot(normal, -ray), 0.0);
  float fresnel = pow(1.0 - facing, 3.0);
  float film = .5 + .5 * sin(facing * 9.0 + sculpt(p, t).z * 5.0 - t * .23);
  vec3 alloy = mix(uAccent, uTint, film * .78);

  // Moving studio softboxes create broad reflections and a fine white rim.
  float key = pow(max(dot(reflection, normalize(vec3(-.55, .8, 1))), 0.0), 18.0);
  float rim = pow(max(dot(reflection, normalize(vec3(.9, -.3, .8))), 0.0), 34.0);
  float strip = exp(-sq((reflection.y + .16 + .12 * sin(reflection.x * 3.0 + t * .19)) / .105));
  strip *= smoothstep(-.45, .4, reflection.z);
  float shade = .17 + .24 * (.5 + .5 * normal.y);
  vec3 color = alloy * shade;
  color += mix(alloy, uPearl, .56) * pow(.5 + .5 * reflection.y, 3.0) * .62;
  color += uPearl * (key * .9 + rim * .85 + strip * .72);
  color += mix(uTint, uPearl, .66) * fresnel * .66;
  color *= .74 + .26 * smoothstep(.24, .85, length(p.xy));
  color = mix(color, uPearl, pow(1.0 - facing, 10.0) * .38);
  color = clamp(color, 0.0, 1.0);
  return over(vec4(color, 1.0), halo);
}
`

const eclipse = `
vec4 accretion(vec2 p, float t, float front) {
  float tilt = .235 + .025 * sin(t * .2);
  vec2 disk = vec2(p.x, p.y / tilt);
  float radius = length(disk);
  float angle = atan(disk.y, disk.x);
  float envelope = smoothstep(.66, .88, radius) * (1.0 - smoothstep(1.12, 1.82, radius));
  float turbulence = field(vec2(radius * 5.0 - t * .18, angle * 2.0 + t * .12));
  float streak = filament(radius * 93.0 + turbulence * 10.0 - t * 1.4, 4.0);
  float spiral = .5 + .5 * sin(angle * 3.0 + radius * 12.0 - t * .9);
  float doppler = .52 + .62 * smoothstep(-1.3, 1.2, -p.x);
  float energy = envelope * (.26 + streak * .95 + spiral * .34) * doppler;
  energy *= mix(smoothstep(-.035, .035, p.y), 1.0 - smoothstep(-.035, .035, p.y), front);
  return light(energy * 1.7, .25 + turbulence * .7);
}
vec4 scene(vec2 q, float t) {
  vec2 p = rotate(-.19 + .035 * sin(t * .16) + uPointer.x * .035) * q;
  float radius = length(p);
  float angle = atan(p.y, p.x);
  float horizon = .575 + .012 * sin(t * .26);
  float distance = abs(radius - horizon);
  float corona = exp(-distance * 8.0) * .30;
  vec4 result = light(corona, .55);
  result = over(accretion(p, t, 0.0), result);

  // A lensed image of the far disk rises above the silhouette.
  vec2 lens = vec2(p.x, p.y * .96 - .018);
  float lensRadius = length(lens);
  float lensWindow = smoothstep(horizon + .008, horizon + .055, lensRadius)
    * (1.0 - smoothstep(.87, 1.14, lensRadius)) * smoothstep(-.05, .28, p.y);
  float lensFlow = field(vec2(atan(lens.y, lens.x) * 4.0 - t * .2, lensRadius * 18.0));
  float arcs = filament(lensRadius * 112.0 + lensFlow * 9.0 - t * 1.1, 3.0);
  result = over(light(lensWindow * (.16 + .8 * arcs), lensFlow), result);

  float pixelWidth = max(.007, uViewport.y / uResolution.y / uScale);
  float silhouette = 1.0 - smoothstep(horizon - pixelWidth, horizon + pixelWidth, radius);
  vec3 obsidian = mix(uAccent * .075, vec3(.006), .72);
  float innerReflection = pow(clamp(radius / horizon, 0.0, 1.0), 13.0) * .06;
  vec3 core = obsidian + uTint * innerReflection;
  result = over(vec4(core * silhouette, silhouette), result);

  // The photon ring stays fine; the glow carries the energy out into space.
  float hotRing = exp(-sq(distance / max(.013, pixelWidth))) * 2.7;
  float ringGlow = exp(-distance * 33.0) * .64;
  float flow = .76 + .24 * sin(angle * 3.0 - t * .55);
  result = over(light((hotRing + ringGlow) * flow, .48 + .38 * sin(angle + t * .15)), result);
  result = over(accretion(p, t, 1.0), result);

  // Two long, soft optical streaks dissolve well beyond the focal object.
  float flare = exp(-sq(p.y / .022)) * exp(-abs(p.x) * 1.9);
  flare *= smoothstep(.65, .9, abs(p.x));
  return over(light(flare * .35, .6), result);
}
`

const aurora = `
vec4 scene(vec2 q, float t) {
  vec2 p = rotate(-.43 + .05 * sin(t * .13) + uPointer.x * .035) * q;
  p.y += uPointer.y * .025;
  vec4 result = vec4(0);
  for (int i = 0; i < 5; i++) {
    float layer = float(i);
    float x = p.x + .09 * sin(t * .25 + layer);
    float phase = t * .38 - layer * .49;
    float center = .32 * sin(x * 1.85 - phase) + .14 * sin(x * 3.3 + phase * .7);
    center += (layer - 2.0) * .095;
    float width = .26 + .12 * sin(x * 1.7 + phase * .53 + layer * .38);
    float v = (p.y - center) / width;
    float taper = 1.0 - smoothstep(.72, 1.78, abs(x + (layer - 2.0) * .075));
    float edge = exp(-pow(abs(v), 3.0) * 1.5);
    float fold = x * 10.0 + v * 1.6 + sin(x * 2.0 + phase) * 2.5 - phase;
    float satin = .5 + .5 * sin(fold);
    float ridge = pow(satin, 9.0);
    float threadPhase = v * 96.0 + sin(x * 4.0 - phase) * 4.0;
    float threads = filament(threadPhase, 12.0);
    float lip = exp(-sq((abs(v) - .67) * 10.0));
    float shimmer = .72 + .28 * sin(x * 2.4 + t * .62 + layer);
    float energy = edge * (.17 + .82 * ridge + .34 * threads) + lip * .27;
    energy *= taper * shimmer * (.52 + layer * .16);
    float tint = .5 + .5 * sin(x * 1.5 + v * .9 + layer * .6 - t * .15);
    result = over(light(energy * 1.65, tint), result);

    // Light blooms out of the folds without turning the entire cloth white.
    float radiance = exp(-sq(v * .52)) * taper * ridge * .07;
    result = over(light(radiance, tint), result);
  }
  return result;
}
`

const main = `
void main() {
  vec2 pixel = gl_FragCoord.xy / uResolution * uViewport;
  pixel.y = uViewport.y - pixel.y;
  float visibility = quietSpace(pixel);
  if (visibility < .001) { fragColor = vec4(0); return; }
  vec2 q = (pixel - uFocus) / uScale;
  q.y = -q.y;
  q -= uPointer * .035;
  float t = uTime;
  float radius = length(q);
  if (radius > 7.0) { fragColor = vec4(0); return; }
  vec4 background = atmosphere(q, t) * (1.0 - smoothstep(4.0, 7.0, radius));
  vec4 artwork = vec4(0);
  if (radius < 3.5) artwork = scene(q, t) * (1.0 - smoothstep(2.8, 3.5, radius));
  vec4 result = over(artwork, background);
  // No illustration-sized mask: only natural light falloff and DOM quiet zones.
  fragColor = result * visibility;
}
`

export function artworkFragment(variant: ArtworkVariant) {
  const scenes = { mercury, eclipse, aurora }
  return common + scenes[variant] + main
}
