"""Generate golden smoothing fixtures from the DJIGyroFix v0.92 reference.

Run from the reference checkout root. Output feeds internal/correct parity tests.
"""
from __future__ import annotations

import json, math, random, struct, sys
from gyrofix.smoothing import smooth_quaternions, angular_acceleration_score


def f32hex(values):
    return struct.pack("<4f", *values).hex()


def f64hex(value):
    return struct.pack("<d", value).hex()


def unit(w, x, y, z):
    n = math.sqrt(w * w + x * x + y * y + z * z)
    return [w / n, x / n, y / n, z / n]


def case_steady_bump(rate=200, seconds=4.0):
    times = [i / rate for i in range(int(rate * seconds) + 1)]
    quats = []
    for t in times:
        a = 0.2 * t
        if 1.8 < t < 2.2:
            a += 0.15 * math.sin((t - 1.8) / 0.4 * math.pi)
        quats.append([math.cos(a / 2), 0.0, math.sin(a / 2), 0.0])
    return times, quats


def case_random_walk(seed, count=1200, rate=199.8, jitter=0.0):
    rng = random.Random(seed)
    times, quats = [], []
    t = 0.0
    q = unit(1, 0, 0, 0)
    for i in range(count):
        times.append(t)
        t += (1.0 / rate) * (1.0 + jitter * (rng.random() - 0.5))
        quats.append(list(q))
        # small random rotation, occasionally violent
        scale = 0.4 if (300 < i < 340) else 0.01
        dw = 1.0
        dx, dy, dz = (rng.gauss(0, scale) for _ in range(3))
        q = unit(
            dw * q[0] - dx * q[1] - dy * q[2] - dz * q[3],
            dw * q[1] + dx * q[0] + dy * q[3] - dz * q[2],
            dw * q[2] - dx * q[3] + dy * q[0] + dz * q[1],
            dw * q[3] + dx * q[2] - dy * q[1] + dz * q[0],
        )
        if rng.random() < 0.08:  # double-cover sign flips, as seen on real tracks
            q = [-c for c in q]
    return times, quats


def case_flip_heavy(count=800, rate=100.0):
    times = [i / rate for i in range(count)]
    quats = []
    for i, t in enumerate(times):
        a = 1.7 * t
        q = [math.cos(a / 2), math.sin(a / 2) * 0.6, math.sin(a / 2) * 0.8, 0.0]
        q = unit(*q)
        if i % 3 == 0:
            q = [-c for c in q]
        quats.append(q)
    return times, quats


CASES = []


def add(name, times, quats, start, end, smoothing_ms, strength):
    out = smooth_quaternions(times, quats, start, end, smoothing_ms=smoothing_ms, strength=strength)
    CASES.append({
        "name": name,
        "start": start, "end": end,
        "smoothing_ms": smoothing_ms, "strength": strength,
        "times": [f64hex(t) for t in times],
        "input": [f32hex(q) for q in quats],
        "input_exact": [[f64hex(c) for c in q] for q in quats],
        "output": [f32hex(q) for q in out],
        "score_before": f64hex(angular_acceleration_score(times, quats, start, end)),
        "score_after": f64hex(angular_acceleration_score(times, out, start, end)),
    })


t, q = case_steady_bump()
add("steady_bump", t, q, 1.0, 3.0, 180.0, 1.0)
add("steady_bump_weak", t, q, 1.5, 2.5, 60.0, 0.35)
add("steady_bump_wide", t, q, 0.05, 3.95, 400.0, 1.0)

t, q = case_random_walk(20260901)
add("random_walk", t, q, 1.0, 4.0, 180.0, 1.0)
add("random_walk_short_window", t, q, 1.4, 1.9, 90.0, 1.0)

t, q = case_random_walk(7, count=900, jitter=0.35)
add("random_walk_jittery_dt", t, q, 0.5, 3.5, 180.0, 0.75)

t, q = case_flip_heavy()
add("sign_flips", t, q, 1.0, 6.0, 180.0, 1.0)
add("sign_flips_tiny_window", t, q, 2.0, 2.08, 180.0, 1.0)

t, q = case_random_walk(99, count=40, rate=200.0)
add("tiny_series", t, q, 0.02, 0.18, 180.0, 1.0)

json.dump({"cases": CASES}, open(sys.argv[1], "w"))
print(f"wrote {len(CASES)} cases")
for c in CASES:
    print(f"  {c['name']}: {len(c['times'])} samples")
