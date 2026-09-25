// Generated card backgrounds. One stitchable function; `family` picks the
// pattern (CardFamily's raw value). All parameters come from CardStyle, so a
// card looks the same every time for the same seed.
#include <metal_stdlib>
#include <SwiftUI/SwiftUI_Metal.h>
#include "SparkNoise.h"
using namespace metal;

// Voronoi: returns (F1, F2) and the id of the nearest cell.
static inline float3 voronoi(float2 p, thread float2 &cellId) {
    float2 n = floor(p);
    float2 f = fract(p);
    float f1 = 8.0, f2 = 8.0;
    for (int j = -1; j <= 1; j++) {
        for (int i = -1; i <= 1; i++) {
            float2 g = float2(i, j);
            float2 o = hash22(n + g);
            float d = length(g + o - f);
            if (d < f1) { f2 = f1; f1 = d; cellId = n + g; }
            else if (d < f2) { f2 = d; }
        }
    }
    return float3(f1, f2, 0.0);
}

[[ stitchable ]] half4 sparkCard(float2 position, half4 color, float2 size, float time, float family,
                                 float warp, float2 offset, float scale, float angle,
                                 half4 c0, half4 c1, half4 c2, half4 c3) {
    float2 uv = position / size;
    float2 p = (position - 0.5 * size) / min(size.x, size.y);
    float ca = cos(angle), sa = sin(angle);
    p = float2(ca * p.x - sa * p.y, sa * p.x + ca * p.y) * scale + offset;
    float t = time * 0.06;
    int fam = int(family + 0.5);
    half3 col;

    if (fam == 0) {
        // Nebula: domain-warped fbm (Inigo Quilez's warp, twice).
        p = (p - offset) * 0.3 + offset;
        float2 q = float2(fbm(p + float2(0.0, t)), fbm(p + float2(5.2, 1.3) - t));
        float2 r = float2(fbm(p + warp * 1.3 * q + float2(1.7, 9.2) + t * 0.7),
                          fbm(p + warp * 1.3 * q + float2(8.3, 2.8) - t * 0.5));
        float v = fbm(p + warp * 1.6 * r) * 0.5 + 0.5;
        col = ramp4(v * 1.15 - 0.05, c0.rgb, c1.rgb, c2.rgb, c3.rgb);
        col = mix(col, c3.rgb, half(saturate(length(q) * 0.35 - 0.1)));
    } else if (fam == 1) {
        // Cells: voronoi with warped edges.
        float2 w = p * 1.4 + warp * 0.35 * float2(snoise(p * 0.7 + t), snoise(p * 0.7 - t + 4.0));
        float2 cid = float2(0.0);
        float3 v = voronoi(w, cid);
        float tone = hash21(cid);
        col = ramp4(0.25 + tone * 0.75, c0.rgb, c1.rgb, c2.rgb, c3.rgb);
        float edge = smoothstep(0.0, 0.08, v.y - v.x);
        col = mix(c0.rgb, col, half(edge));
        col *= half(0.8 + 0.35 * (1.0 - v.x));
    } else if (fam == 2) {
        // Mesh: four drifting control points blended by inverse distance.
        float2 u = uv;
        float2 a = float2(0.2, 0.2) + 0.15 * float2(sin(t * 3.0 + offset.x), cos(t * 2.0 + offset.y));
        float2 b = float2(0.85, 0.25) + 0.15 * float2(cos(t * 2.5 + offset.y), sin(t * 3.1));
        float2 c = float2(0.25, 0.85) + 0.15 * float2(sin(t * 2.2), cos(t * 2.7 + offset.x));
        float2 d = float2(0.8, 0.8) + 0.15 * float2(cos(t * 3.3 + warp), sin(t * 2.4));
        float2 wu = u + warp * 0.06 * float2(snoise(u * 3.0 + offset), snoise(u * 3.0 - offset));
        float wa = 1.0 / pow(length(wu - a) + 0.05, 2.0);
        float wb = 1.0 / pow(length(wu - b) + 0.05, 2.0);
        float wc = 1.0 / pow(length(wu - c) + 0.05, 2.0);
        float wd = 1.0 / pow(length(wu - d) + 0.05, 2.0);
        float sum = wa + wb + wc + wd;
        col = (c0.rgb * half(wa) + c1.rgb * half(wb) + c2.rgb * half(wc) + c3.rgb * half(wd)) / half(sum);
    } else if (fam == 3) {
        // Contour: topographic lines over warped fbm.
        float v = fbm(p * 0.8 + warp * 0.6 * float2(fbm(p + t), fbm(p - t))) * 0.5 + 0.5;
        float bands = v * 9.0;
        float line = 1.0 - smoothstep(0.0, 0.09, abs(fract(bands) - 0.5) - 0.41);
        col = ramp4(v, c0.rgb, c1.rgb, c1.rgb, c2.rgb);
        col = mix(col, c3.rgb, half(line * 0.85));
    } else if (fam == 4) {
        // Ripple: interference of three wave sources.
        float v = 0.0;
        for (int i = 0; i < 3; i++) {
            float2 src = hash22(offset + float2(i, i * 3.0)) * 2.0 - 1.0;
            v += sin(length(p - offset - src * 1.5) * (7.0 + warp * 4.0) - time * 0.8 + float(i));
        }
        v = v / 6.0 + 0.5;
        col = ramp4(v, c0.rgb, c1.rgb, c2.rgb, c3.rgb);
    } else {
        // Aurora: curtains of light over a dark ground.
        col = c0.rgb;
        for (int i = 0; i < 3; i++) {
            float fi = float(i);
            float y = 0.3 + 0.2 * fi + 0.12 * fbm(float2(uv.x * 1.8 + offset.x + fi * 3.1, t + fi));
            float band = exp(-abs(uv.y - y) * (9.0 - warp * 3.0));
            float shimmer = 0.6 + 0.4 * snoise(float2(uv.x * 14.0 + fi * 5.0, t * 4.0));
            half3 tint = fi < 0.5 ? c1.rgb : (fi < 1.5 ? c2.rgb : c3.rgb);
            col += tint * half(band * shimmer * 0.9);
        }
    }

    // Film grain and a soft vignette keep flat areas from banding.
    float grain = (hash21(position + fract(time)) - 0.5) * 0.035;
    float vig = 1.0 - 0.28 * pow(length(uv - 0.5) * 1.3, 2.0);
    col = col * half(vig) + half3(grain);
    return half4(saturate(col), 1.0) * color.a;
}
