import Arshes;

Texture2D<float4> cameraTex : register(t0);
Texture2D<float2> depthTex : register(t1);
SamplerState samp : register(s0);

uniform float time;

// Gerstner wave - physically based ocean wave
float3 gerstnerWave(float2 pos, float2 dir, float steepness, float wavelength, float t) {
    float k = 2.0 * 3.14159 / wavelength;
    float c = sqrt(9.8 / k);
    float2 d = normalize(dir);
    float f = k * (dot(d, pos) - c * t);
    float a = steepness / k;

    return float3(
        d.x * a * cos(f),
        a * sin(f),
        d.y * a * cos(f)
    );
}

// Calculate water surface position with multiple Gerstner waves
float3 waterSurface(float2 pos) {
    float t = time * 0.8;
    float3 p = float3(pos.x, 0.0, pos.y);

    // Layer multiple waves with different directions and frequencies
    p += gerstnerWave(pos, float2(1.0, 0.0), 0.15, 4.0, t);
    p += gerstnerWave(pos, float2(1.0, 0.6), 0.12, 2.5, t * 1.1);
    p += gerstnerWave(pos, float2(0.3, 1.0), 0.10, 1.8, t * 0.9);
    p += gerstnerWave(pos, float2(-0.5, 0.7), 0.08, 1.2, t * 1.2);
    p += gerstnerWave(pos, float2(-0.8, -0.2), 0.06, 0.8, t * 1.4);
    p += gerstnerWave(pos, float2(0.2, -0.9), 0.05, 0.5, t * 1.6);

    return p;
}

// Calculate normal from water surface
float3 waterNormal(float2 pos) {
    float eps = 0.01;

    float3 center = waterSurface(pos);
    float3 right = waterSurface(pos + float2(eps, 0.0));
    float3 forward = waterSurface(pos + float2(0.0, eps));

    float3 tangent = right - center;
    float3 bitangent = forward - center;

    return normalize(cross(bitangent, tangent));
}

// Schlick's approximation for Fresnel
float fresnelSchlick(float cosTheta, float F0) {
    return F0 + (1.0 - F0) * pow(1.0 - cosTheta, 5.0);
}

// Water absorption based on depth
float3 waterAbsorption(float depth) {
    // Water absorbs red first, then green, blue travels furthest
    float3 absorptionCoeff = float3(0.45, 0.09, 0.06);
    return exp(-absorptionCoeff * depth);
}

[shader("fragment")]
float4 fragmentMain(float2 uv : TEXCOORD) : SV_Target {
    // Convert UV to world-like coordinates for wave calculation
    float2 worldPos = (uv - 0.5) * 10.0;

    // Get water surface normal
    float3 normal = waterNormal(worldPos);

    // View direction (looking down at water surface)
    float3 viewDir = normalize(float3(0.0, 1.0, 0.3));

    // Fresnel calculation
    float NdotV = max(dot(normal, viewDir), 0.0);
    float fresnel = fresnelSchlick(NdotV, 0.02);

    // Refraction
    float3 refractDir = refract(-viewDir, normal, 1.0 / 1.33);
    float2 refractOffset = refractDir.xz * 0.05;

    // Depth-based refraction strength
    float waterHeight = waterSurface(worldPos).y;
    float depthFactor = 1.0 + waterHeight * 0.5;
    refractOffset *= depthFactor;

    // Sample refracted image with chromatic dispersion
    float2 uvR = uv + refractOffset * 1.02;
    float2 uvG = uv + refractOffset;
    float2 uvB = uv + refractOffset * 0.98;

    uvR = clamp(uvR, 0.0, 1.0);
    uvG = clamp(uvG, 0.0, 1.0);
    uvB = clamp(uvB, 0.0, 1.0);

    float3 refractedColor;
    refractedColor.r = cameraTex.Sample(samp, uvR).r;
    refractedColor.g = cameraTex.Sample(samp, uvG).g;
    refractedColor.b = cameraTex.Sample(samp, uvB).b;

    // Apply water absorption to refracted light
    float absorptionDepth = 2.0 - waterHeight * 0.5;
    float3 absorption = waterAbsorption(absorptionDepth);
    refractedColor *= absorption;

    // Deep water color
    float3 deepWaterColor = float3(0.01, 0.05, 0.12);
    refractedColor = lerp(deepWaterColor, refractedColor, 0.7 + NdotV * 0.3);

    // Reflection
    float3 reflectDir = reflect(-viewDir, normal);

    // Sample reflected image
    float2 reflectUV = uv + reflectDir.xz * 0.08;
    reflectUV.y = 1.0 - reflectUV.y;
    reflectUV = clamp(reflectUV, 0.0, 1.0);
    float3 reflectedColor = cameraTex.Sample(samp, reflectUV).rgb;

    // Sky gradient for reflection
    float skyGradient = smoothstep(-0.2, 0.8, reflectDir.y);
    float3 skyColor = lerp(float3(0.6, 0.7, 0.8), float3(0.4, 0.6, 0.9), skyGradient);
    reflectedColor = lerp(reflectedColor * 0.5, skyColor, 0.6);

    // Combine reflection and refraction using Fresnel
    float3 col = lerp(refractedColor, reflectedColor, fresnel);

    // Sun specular highlight
    float3 sunDir = normalize(float3(0.5, 0.8, 0.3));
    float3 halfVec = normalize(sunDir + viewDir);
    float specular = pow(max(dot(normal, halfVec), 0.0), 256.0);
    float3 sunColor = float3(1.0, 0.95, 0.85);
    col += sunColor * specular * 2.0;

    // Secondary specular (broader)
    float specular2 = pow(max(dot(normal, halfVec), 0.0), 32.0);
    col += sunColor * specular2 * 0.15;

    // Subsurface scattering approximation
    float sss = pow(max(dot(viewDir, -sunDir + normal * 0.5), 0.0), 3.0);
    col += float3(0.0, 0.15, 0.2) * sss * (1.0 - fresnel) * 0.5;

    // Subtle wave foam at peaks
    float foamThreshold = 0.25;
    float foam = smoothstep(foamThreshold, foamThreshold + 0.1, waterHeight);
    foam *= smoothstep(0.0, 0.3, 1.0 - NdotV);
    col = lerp(col, float3(0.9, 0.95, 1.0), foam * 0.3);

    // Tone mapping
    col = col / (col + float3(1.0, 1.0, 1.0));

    // Slight color grading
    col = pow(col, float3(0.95, 0.97, 1.0));

    return float4(col, 1.0);
}
