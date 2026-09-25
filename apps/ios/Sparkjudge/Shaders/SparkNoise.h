// Noise shared by the orb and card shaders. Everything is `static inline` so
// both .metal files can include it without clashing at link time.
#ifndef SPARK_NOISE_H
#define SPARK_NOISE_H

#include <metal_stdlib>
using namespace metal;

static inline float hash21(float2 p) {
    p = fract(p * float2(123.34, 456.21));
    p += dot(p, p + 45.32);
    return fract(p.x * p.y);
}

static inline float2 hash22(float2 p) {
    float3 a = fract(float3(p.x, p.y, p.x) * float3(123.34, 234.34, 345.65));
    a += dot(a, a + 34.45);
    return fract(float2(a.x * a.y, a.y * a.z));
}

static inline float3 permute3(float3 x) { return fmod(((x * 34.0) + 1.0) * x, 289.0); }

// 2D simplex noise (Ashima Arts / Stefan Gustavson), roughly in [-1, 1].
static inline float snoise(float2 v) {
    const float4 C = float4(0.211324865405187, 0.366025403784439, -0.577350269189626, 0.024390243902439);
    float2 i = floor(v + dot(v, C.yy));
    float2 x0 = v - i + dot(i, C.xx);
    float2 i1 = (x0.x > x0.y) ? float2(1.0, 0.0) : float2(0.0, 1.0);
    float4 x12 = x0.xyxy + C.xxzz;
    x12.xy -= i1;
    i = fmod(i, 289.0);
    float3 p = permute3(permute3(i.y + float3(0.0, i1.y, 1.0)) + i.x + float3(0.0, i1.x, 1.0));
    float3 m = max(0.5 - float3(dot(x0, x0), dot(x12.xy, x12.xy), dot(x12.zw, x12.zw)), 0.0);
    m = m * m;
    m = m * m;
    float3 x = 2.0 * fract(p * C.www) - 1.0;
    float3 h = abs(x) - 0.5;
    float3 ox = floor(x + 0.5);
    float3 a0 = x - ox;
    m *= 1.79284291400159 - 0.85373472095314 * (a0 * a0 + h * h);
    float3 g;
    g.x = a0.x * x0.x + h.x * x0.y;
    g.yz = a0.yz * x12.xz + h.yz * x12.yw;
    return 130.0 * dot(m, g);
}

// Fractional Brownian motion: five octaves of simplex noise, roughly in [-1, 1].
static inline float fbm(float2 p) {
    float f = 0.0;
    float a = 0.5;
    for (int i = 0; i < 5; i++) {
        f += a * snoise(p);
        p = float2x2(1.6, 1.2, -1.2, 1.6) * p + 17.0;
        a *= 0.5;
    }
    return f;
}

// Four-stop color ramp over t in [0, 1].
static inline half3 ramp4(float t, half3 c0, half3 c1, half3 c2, half3 c3) {
    t = saturate(t) * 3.0;
    if (t < 1.0) return mix(c0, c1, half(t));
    if (t < 2.0) return mix(c1, c2, half(t - 1.0));
    return mix(c2, c3, half(t - 2.0));
}

#endif
