// The dictate orb: a noisy sphere SDF whose rim and swirl follow the
// microphone level, with a soft glow around it. Used as a SwiftUI colorEffect.
#include <metal_stdlib>
#include <SwiftUI/SwiftUI_Metal.h>
#include "SparkNoise.h"
using namespace metal;

[[ stitchable ]] half4 sparkOrb(float2 position, half4 color, float2 size, float time, float level,
                                half4 inner, half4 outer, half4 glow) {
    float s = min(size.x, size.y);
    float2 p = (position - 0.5 * size) / s;          // -0.5 … 0.5 across the short side
    float lv = saturate(level);

    float r0 = 0.24 + 0.05 * lv;
    float ang = atan2(p.y, p.x);
    float2 around = float2(cos(ang), sin(ang));
    // Rim wobble: slow breathing when quiet, sharp ripples when loud.
    float wob = snoise(around * (1.1 + 2.2 * lv) + float2(time * 0.35, -time * 0.5)) * (0.012 + 0.055 * lv)
              + snoise(around * 5.0 + time * (0.8 + 2.0 * lv)) * 0.018 * lv;
    float d = length(p) - r0 - wob;

    // Fake sphere depth for shading.
    float z = sqrt(saturate(1.0 - dot(p, p) / (r0 * r0)));
    float swirl = fbm(p * (3.0 + 2.0 * lv) + float2(time * 0.22, -time * 0.17) + z * 1.3);
    half3 body = mix(outer.rgb, inner.rgb, half(saturate(0.55 + 0.6 * swirl - p.y * 1.4)));
    float rim = pow(1.0 - z, 2.2);
    body += glow.rgb * half(rim * (0.6 + 0.8 * lv));
    float spec = pow(saturate(1.0 - length(p - float2(-0.08, -0.1)) / (r0 * 0.55)), 3.0);
    body += half3(spec * 0.45);

    float aa = 1.5 / s;
    float inside = 1.0 - smoothstep(-aa, aa, d);
    float g = exp(-max(d, 0.0) * (22.0 - 10.0 * lv)) * (0.28 + 0.75 * lv);
    g *= 0.85 + 0.15 * snoise(p * 6.0 + time);
    // Fade out before the frame's edge so the glow never shows a square.
    g *= 1.0 - smoothstep(0.36, 0.5, length(p));

    // Premultiplied: body over glow.
    half3 rgb = body * half(inside) + glow.rgb * half(g * (1.0 - inside));
    half a = half(inside + g * (1.0 - inside));
    return half4(rgb, a) * color.a;
}
