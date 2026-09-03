# Real-footage findings

This document records what changed when djgyrofix was exercised end to end on
two real O4 clips, neither committed: `testdata/DJI_20260705_RAW.MP4`
(6,570,736,456 bytes, the clip with the reported artifacts) and
`testdata/DJI_20260830_VILLA2.MP4` (3,848,754,322 bytes, from an O4 Pro, which
its owner considers entirely normal and in no need of correction). The historical implementation is preserved on the `study`
branch; `main` contains the resulting detector and correction pipeline.

## The artifact model

Frame vibration recorded into the attitude track. It survives as brief
excursions from the local trend — deviations the aircraft did not make, sitting
on top of the motion it did.

This is a correction. The model here was originally written as "a fast but
comparatively low-frequency, out-of-sync attitude response around sharp
motion-vector changes", from measurements on a single clip taken before the
sample duplication below was understood. Two things narrow that claim too far.
The duplication inflated the residual in proportion to rotation rate, which put
the apparent artifact where the aircraft was turning hardest and made the
association with vector changes look stronger than it is. And community reports
of the same symptom span frames, builds and flying styles, describing vibration
sensitivity rather than anything specific to turns.

What is measured here, with the duplication collapsed: the per-sample residual
crosses four times its median in 3,145 runs across the clip, median length
4.04 ms, p90 25.28 ms. Short, frequent, and distributed rather than confined to
manoeuvres. Where the deviation points matters more than when it happens — a
residual across the local rotation axis is the axis wobbling, which is the
artifact; a residual along it is the aircraft turning faster or slower than the
trend, which is flying.

That distinction is what detection rests on, because a plain angular-rate
threshold cannot separate the artifact from an intentional whip, impact or
reversal. Plausible rapid motion may be smoothed when its residual shape
warrants it, but it is never reconstructed as missing orientation.

## What the original pass got wrong

Five issues became material on the full clip:

1. A detected bin at the threshold received zero correction, and a bin only
   slightly above it received very little. Blur reduced that weight further, so
   most detected events survived correction.
2. Two nearby over-full-scale transitions were treated as a corrupt run without
   proving the second transition returned to the pre-entry trajectory. One
   continuous burst around 257.2 seconds became a dozen false dropouts.
3. A bridge interpolated from the original series after surrounding smoothing
   had already run. That could create new discontinuities at the bridge edges.
4. An event touching the final bin could end after the last quaternion, making a
   scan report unusable as a manual `--ranges` input.
5. Event scoring normalized and traversed the complete quaternion track for
   every event, making the reporting work proportional to events × quaternions.

## Implementation consequences

- A confirmed event has one non-zero correction weight across its core. Peak
  excess can raise that weight, and fixed exterior shoulders taper it smoothly.
  This avoids both under-correction and artificial 10 ms blend steps.
- An over-rate pair is bridgeable only if it is opposing, returns near the
  pre-entry orientation and settles afterwards. A lone or continuing rapid move
  remains real motion.
- All accepted corrupt samples are bridged into a working quaternion series
  first. Smoothing then reads and writes that same series.
- Reported times are clamped to the first and last quaternion timestamps.
- Angular-acceleration data is prepared once for each before/after series;
  individual event scores query indexed spans.
- Correction is followed by at most three bounded rescans. A newly exposed
  smoothing event may be included only when it is adjacent to an authorized
  correction scope and the union remains within `--max-affected`. A newly
  detected dropout is never bridged automatically.

## Full-clip result

The test clip contains 993,523 quaternions at 1978 Hz across 8 minutes 17
seconds of video. The current pipeline produced:

| Check | Result |
|---|---|
| Reported bounded analysis | 85 events: 73 jitter, 12 impact, 0 dropout |
| Affected duration | 30.6828375 seconds, 6.1703% of the clip |
| Bounded discoveries | 3 smoothing regions exposed during correction |
| Patch | 102,824 quaternions in 3,164 metadata samples |
| Writes | 411,268 four-byte writes; 1,645,072 bytes journalled |
| Transient score | Reduced by 91.598% |
| Rescan of patched copy | No events |
| Verify | All ranges, metadata digest and 6,570,736,456-byte size passed |
| Revert | Restored bytes compared identical with the original |

The earlier gate classified 17 runs as dropouts. The return-and-settle test
rejects all 17 as real rapid motion on this clip. That is a safer failure mode:
smoothing a detected deviation can be undone, while bridging real motion invents
an orientation the camera did not report.

## The baseline window, and a burst that hid itself

A viewer reported sharp jitter surviving correction at 1:19-1:21. It was there,
and the cause was detection rather than correction: of that two-second window,
0.23 s had ever been flagged.

The residual over 79.0-80.0 s runs 165-235 °/s. The threshold over the same
span is about 605 °/s, from `baseline + 5·sigma` with a baseline of 94 °/s and a
sigma near 102. The window is a ±5 s half-width, so it spans 10 s, and a 2 s
burst supplies a fifth of the samples that sigma is computed from. The burst
raises the very bar meant to catch it. Seven of 200 bins crossed.

Widening the window is what fixes it, and it is cheaper than loosening the
multiplier:

| Setting | Events | Affected | 79-81 s covered |
|---|---|---|---|
| ±5 s (the old default) | 82 | 5.98% | 12% |
| ±20 s | 91 | 7.61% | 46% |
| ±20 s, `--mad-k 4` | 99 | 9.87% | 54% |
| ±20 s, `--mad-k 3` | 107 | 12.73% | 77% |
| `--mad-k 2` alone | 115 | 14.74% | 37% |
| ±2 s | 66 | 4.36% | 8% |

Measured residual in the reported window, before and after correction:

| | RMS | Peak |
|---|---|---|
| Original | 238.6 °/s | 2009.1 °/s |
| Corrected at ±5 s | 171.3 °/s | 869.1 °/s |
| Corrected at ±20 s, `--mad-k 3` | 14.3 °/s | 204.4 °/s |

Two consequences followed. The window became a named flight style, because its
right value depends on how the aircraft was flown rather than on anything a
pilot can measure. And the window is now capped at a quarter of the clip
duration: a half-width that reaches past half the footage is not rolling any
more, and on a thirty-second clip a ±12 s window flattened localized roughness
back into the clip's own level — the exact failure the rolling baseline replaced.

## What the in-region score concealed

The same run reported a 91.6% residual reduction while that window still shook.
The figure was true and useless. It is measured inside the corrected regions,
and those regions covered 12% of the burst, so it was reporting the quality of
the correction over the ground detection had chosen to look at. It cannot fall
where nothing was flagged, which makes it most flattering exactly when
under-detection is the problem.

The same run measures 16.1% clip-wide on the same metric. Both are now printed,
clip-wide first.

## DJI stores every attitude twice

The largest error in this tool was not in the detector. It was in the first
line of the pipeline.

A viewer reported that a patched file changed nothing in Gyroflow. Gyroflow's
own parser, `telemetry-parser` at the revision Gyroflow pins, reads the same
field this tool patches — for wm169, `frame_meta(3) → imu_frame_meta(3) →
IMU_attitude_after_fusion(2) → attitude`, which is the `3.3.2.3` path here — and
dumping it against the original and the patched copy confirms it sees the patch.
The plumbing was never the problem.

What that dump also showed is that every quaternion appears twice:

```
79.0001  -0.318590030 -0.118440285  0.099368557  0.935199514
79.0006  -0.318590030 -0.118440285  0.099368557  0.935199514
79.0011  -0.320291102 -0.118514359  0.099473476  0.934597731
79.0016  -0.320291102 -0.118514359  0.099473476  0.934597731
```

Measured over both clips, it is structural rather than a defect of either:

| Clip | Quaternions | Identical to predecessor | Run lengths |
|---|---|---|---|
| RAW | 993,523 @ 1978 Hz | 496,759 — 50.00% | 496,759 of length 2, 5 others |
| VILLA2 | 455,517 @ 1978 Hz | 227,756 — 50.00% | 227,756 of length 2, 5 others |

The duplicates sit at a fixed parity — 496,733 even against 26 odd on RAW. DJI
presents 1978 Hz while carrying 989 Hz of information.

**It is the frame rate that decides this, not the format.** Every clip measured
packs about 33.3 quaternion slots into each video frame and fills them from an
IMU running near 1000 Hz, so the repeat factor is `33.3 x fps / 1000`. Both air
units are 59.94 fps and pad by exactly two; the 29.97 fps Osmo pads by exactly
one and has a duplicate share of 0.00. `info` now prints the share and the
information rate it implies, because a new camera is where this changes.

| fps | stored | padding | duplicate share |
|---|---|---|---|
| 30 | ~999 Hz | x1.00 | 0.00 (Osmo, measured) |
| 48 | ~1598 Hz | x1.60 | 0.37 |
| 50 | ~1665 Hz | x1.66 | 0.40 |
| 60 | ~1998 Hz | x2.00 | 0.50 (both air units, measured) |
| 120 | ~3996 Hz | x4.00 | 0.75 |

Only 30 and 60 have been seen, and the threshold that decides whether to collapse
the repeats was originally set from those two alone at 0.40 — which sits directly
on top of the 48 and 50 fps cases. Both are ordinary shooting modes, and either
would have been left uncollapsed and differenced into the square wave described
below. The threshold is now 0.15, six orders of magnitude above a genuine frozen
dropout and less than half the smallest real padding.

Collapsing a fractional ratio is an improvement rather than a cure. A whole-number
ratio gives runs of one length and the floor returns to where an unpadded stream
sits; a fractional one alternates between run lengths, so the interval between
distinct orientations keeps moving and some of that survives. Against a 0.56 °/s
unpadded floor, a 1.6 ratio measures 4.20 °/s uncollapsed and 2.29 collapsed.
Closing the rest would mean resampling onto the distinct orientations instead of
holding velocity across the repeats, which no clip here justifies — nothing at 48
or 50 fps has been seen.

Differencing consecutive stored samples, which is what this tool did, turns that
into a square wave at Nyquist. Every other velocity is exactly zero and the rest
are twice the true rate:

```
79.0005   410.57 °/s
79.0010     0.00
79.0015   411.33
79.0020     0.00
```

Its amplitude scales with how fast the aircraft is rotating, so it is largest
exactly where the detector is looking. Collapsing it changes the measurements
the whole tool is built on:

| Measurement | As differenced before | Differenced properly |
|---|---|---|
| RAW, whole-clip residual RMS | 118.4 °/s | 31.6 °/s |
| RAW, a quiet stretch at 300-310 s | 91.2 °/s | 19.7 °/s |
| RAW, apparent noise floor (p50) | 37.9 °/s | 3.4 °/s |
| RAW, median detection threshold | 222 °/s | 60 °/s (the absolute floor) |
| VILLA2, whole-clip residual RMS | 22.6 °/s | 13.4 °/s |
| VILLA2, apparent noise floor (p50) | 10.5 °/s | 2.0 °/s |

Three earlier conclusions in this document and in the README were wrong because
of it:

- That real footage carries a far higher residual floor than a generated clean
  track. It does not. RAW reads 3.4 °/s and VILLA2 2.0 °/s once the duplication
  is accounted for; the 39 °/s that the `upstream` threshold was calibrated
  against was roughly 92% sampling artifact.
- That the burst at 79-81 s was mostly transient artifact. About half of it was
  the square wave. What remains is real and now stands far clearer of the floor:
  124.9 °/s RMS against a quiet-stretch level near 20.
- That the rolling baseline window needed widening. It needed widening only
  because the phantom noise inflated the local sigma. With the duplication
  collapsed, the threshold on RAW sits at the absolute floor everywhere and the
  window makes no measurable difference at any style.

The collapse is decided per file from the whole-stream duplicate share, not
applied blindly, because a short run of identical orientations is also what a
frozen telemetry dropout looks like. Locally the two are the same shape; only
the global statistic separates half a file at a fixed parity from two samples
in thousands.

### What the two clips report afterwards

| Clip | Events | Affected | Noise floor p50 | Verdict |
|---|---|---|---|---|
| RAW | 156 | 5.42% | 3.4 °/s | `patch` |
| VILLA2 | 1 | 0.04% | 2.0 °/s | `patch` |

VILLA2 is the useful control: a clip its owner considers normal now yields a
single 86 ms event over 228 seconds. Before the duplication was handled it
yielded two events and an apparent floor of 10.5 °/s.

## What the reference does, and what it shares

The Python reference, `kim2160/DJIGyroFix` v0.92, was re-read after the
duplication was found. Two things came out of it.

**It carries the same blind spot.** `gyrofix/detection.py` computes velocity the
way this port did until 0.5.0:

```python
for index in range(1, len(quaternions)):
    velocities.append(_rotation_velocity(
        quaternions[index - 1], quaternions[index], ...))
```

Consecutive stored samples, so the same square wave at Nyquist. Its detection
runs over a hand-picked window rather than a whole file, which limits the blast
radius, but the residual it measures inside that window is contaminated the same
way.

**Its quality metric is almost entirely the duplication.**
`angular_acceleration_score` differences consecutive normalized quaternions and
takes the median of the result. On the real clip that metric reads:

| Window | As stored | Duplicates dropped | Share that was the repeats |
|---|---|---|---|
| Whole clip | 2469.5 | 6.8 | 99.7% |
| 79-81 s | 12030.3 | 23.1 | 99.8% |
| Quiet stretch, 300-310 s | 2762.6 | 7.6 | 99.7% |

Smoothing removes the stair-step whether or not it removes any jitter, so the
score falls dramatically either way. That is how a run could report a 91.6%
reduction and look identical in Gyroflow: the number was real, and it was
measuring an achievement nobody could see.

The reported figures are now measured on distinct orientations, with one index
mask taken from the before-series and applied to both so they share a time grid.
`correct.AngularAccelerationScore` keeps the reference's exact behaviour,
because the golden fixtures pin it. Honest numbers on the two real clips:

| Clip | In corrected regions | Clip-wide |
|---|---|---|
| RAW | 91.4% | 9.8% |
| VILLA2 | 99.3% | 0.1% |

**What is actually shared.** The reference is a three-pass box blur over the
quaternion components inside a user-selected range, with smoothstep edge
blending back to the source at the boundaries and an optional strength slerp.
That correction core is what this port holds byte-for-byte parity with, and it
still does: 72 of 72 cases pass against v0.92 after every change described in
this document. Everything else here — whole-file detection, the plausibility
gate, dropout bridging, the weight envelope, the bounded rescans, the diagnosis
— is this port's, and none of it is covered by that parity.

## Mixed-axis event admission validation

The 0.9.1 event-level across/total ratio guard correctly stopped a pure
along-axis control input from being smoothed, but the ratio alone had a mixed
case: a genuine across-axis artifact could fall below 35% of the total when a
stronger control input occurred at the same time. A generated regression fixture
now combines a 150 °/s across-axis oscillation with a 900 °/s along-axis rate
change. The across component clears the ordinary 60 °/s detection threshold on
its own, and remains actionable; the pure along-axis and whip-pan controls still
produce no corrective action.

The narrower rule was compared with the published 0.9.1 binary on both real
clips. Initial detection is unchanged byte for byte at the report-event level:

| Clip | 0.9.1 | Candidate | Affected |
|---|---|---|---|
| RAW | 93 events: 69 jitter, 12 impact, 12 motion | identical | 19.8228375 s (3.9864%) |
| VILLA2 | 1 impact | identical | 0.0863583 s (0.0379%) |

On the RAW dry-run, the bounded correction rescans replace 49 additional short
runs (90 quaternions) without widening the initial authorized scope or changing
the 39,542 quaternions and 1,385 metadata samples ultimately written. The
clip-wide predicted reduction moves only from 4.9962% to 4.9993%. VILLA2's dry
run is identical throughout. The RAW diagnostic warning rises from 42 to 43
original regions still detectable after the three bounded passes, so the tiny
metric improvement is not evidence of full convergence. This validates
detection and correction scope; a stabilized visual comparison still needs to
be watched before tagging because no numerical gyro metric substitutes for the
rendered result.

## Why the residual warning cannot reach zero

A patched run of the 497 s clip ends on `43 original correction region(s) remain
detectable after 3 bounded pass(es)`. That reads as unfinished work. It is not.
Raising the cap from one pass to eight, everything else held at the defaults:

| passes | regions left | quaternions written | in-region | clip-wide | severity ≥ 9 |
|---|---|---|---|---|---|
| 1 | 59 | 132,952 | 75.3% | 4.4% | 36 |
| 2 | 49 | 147,043 | 82.8% | 4.8% | 21 |
| **3** | **43** | **158,153** | **84.7%** | **5.0%** | **14** |
| 4 | 39 | 167,146 | 85.5% | 5.1% | 12 |
| 5 | 34 | 175,938 | 86.8% | 5.1% | 11 |
| 6 | 30 | 180,743 | 87.0% | 5.2% | 10 |
| 7 | 29 | 180,968 | 87.1% | 5.3% | 6 |
| 8 | 28 | 183,188 | 87.3% | 5.2% | 6 |

The count falls monotonically and never oscillates, so the cap is not hiding an
unstable loop. It also never reaches zero. Three passes reach 84.7% of an
achievable 87.3%, which is 97% of the ceiling; passes four through eight rewrite
16% more of the file to buy 2.6 further percentage points in region and 0.2
clip-wide, the latter inside the noise — it drops again at eight. No pass
discovers a new event and the affected span stays at 19.823 s throughout, so the
extra work is entirely additional correction inside the regions already chosen.
Three is where the curve flattens, and it stays.

The interesting part is that the count keeps falling long after the measurement
it is supposed to stand in for has stopped moving. Between three passes and
eight, the region count improves by 35% while in-region residual improves by
three points. The count is not tracking correction quality.

Nor does it track severity. Splitting the 81 corrected regions at the shipped
cap by whether they still trip detection afterwards:

| outcome | n | median severity | median peak | median duration |
|---|---|---|---|---|
| still detectable | 43 | 10.0 | 309.2 °/s | 0.200 s |
| cleared | 38 | 10.0 | 398.2 °/s | 0.200 s |

The regions that *cleared* were the harder ones by peak rate. Whether a region
falls below the detector afterwards is close to uncorrelated with how bad it was
to begin with.

The surviving residual is not a windowing artifact either. Locating each
residual peak within the region it was aimed at puts 19 in the outer quarters
and 21 in the middle half — equal widths, so effectively uniform — with 3
outside. A longer window has nothing specific to reach for. Those regions had
their peak rate cut by a median 35% and remained above threshold anyway, which
is what a bounded correction aimed at a genuinely large transient looks like.

**Consequence for the advice, since applied.** `--sensitivity 1.3` was suggested
whenever any region remained detectable. On this clip that condition holds at
every cap tested, so the tool recommended correcting harder off a number that
more correction demonstrably does not fix in any way the residual metric
registers. It now shares the in-region gate the `--smoothing-ms` suggestion
already used, so it fires only when the correction also missed what it aimed at,
and the warning states where the bounded correction settles instead of counting
defects. Its old justification — that the leftover residual sat at the region
edges and wanted more weight there — was removed rather than reworded, because
the peak locations above do not support it.

## Gyroflow's glitch filtering, and where it does not overlap

Gyroflow added glitch filtering in [`7ac9d110`][gyroflow-glitch] (12 July 2026,
credited in-comment to Gene Matocha), exposed as a strength slider. Its
algorithm is close enough to this one to be worth stating: a high-pass residual
against a short local trend, samples flagged above a multiple of the file's own
baseline, each flagged core grown outward through its ringdown tail, the bad
span bridged by SLERP between the last good and first good sample, repeated for
a couple of passes so anomalies masked by larger ones surface. That is the same
shape as §5 and §6 here, arrived at independently, which is reassuring about the
shape.

The strength slider maps onto three parameters: an absolute floor of 618 °/s at
0%, 195 at 50% and 62 at 100%; a maximum region duration of 0.75 s, 1.5 s and
2.25 s; and 1, 2 or 4 passes.

On `DJI_20260705_RAW.MP4` it changes little, and the reason is structural rather
than a matter of tuning.

**It bridges, and this clip has nothing to bridge.** Its model is a short burst
of *corrupt* attitude data, replaced by the smooth path the camera would have
taken. This clip contains 93 events: 81 smoothing, 12 rejected as intentional
motion, and **no dropouts and no plausibility-gate failures at all**. Nothing in
it is corrupt. It is valid orientation carrying an excessive transient response,
which wants attenuating rather than replacing — SLERP-replacing a median 0.200 s
jitter burst would erase the real motion inside it, which is the case the 30 ms
run cap and the endpoint-match guard of §6 exist to refuse.

**Its threshold is file-global, on a clip whose artifacts help set it.** Flagging
at a multiple of the file's own 99th-percentile residual assumes the artifacts
are rare enough not to define that percentile. Here 4% of the clip is affected
across 81 actionable events in 497 s, so they are a substantial part of the tail
that sets their own bar. The rolling baseline of §5.2 exists for exactly this,
and the absolute floor is not what rejects them: event peaks run p50 339, p90
539, p99 889 °/s, so the default 195 °/s floor admits 67 of the 81 on its own.
The maximum-duration guard is not binding either — the longest actionable event
is 1.100 s against a 1.5 s default.

The two tools therefore address adjacent halves of the same problem. Gyroflow
now repairs corrupt-sample bursts at stabilization time, which is the better
place to do it when that is the defect. This tool repairs the same bursts in the
metadata, and additionally covers the sustained-transient case that a
bridge-only model does not describe. Neither result has been checked against the
other's own test material, and the comparison above rests on one clip.

[gyroflow-glitch]: https://github.com/gyroflow/gyroflow/commit/7ac9d110

## There is no sub-sample timing to find

DESIGN §13.2 asked whether the several quaternions in a metadata sample carry
real timing, or whether the reference's linear interpolation across the sample
is the best available. Walking every quaternion message in both clips settles
it: **993,523 on RAW and 455,517 on VILLA2, and every one has the same shape** —
fields 1 to 4, all `fixed32`, and nothing else at all.

```
3.3.2.#1  varint  87429172        <- microsecond timestamp, per sample
3.3.2.#2  varint  2494            <- sample counter, per sample
3.3.2.#3  message (20 bytes)      <- quaternion 1 of ~33
  3.3.2.3.#1  fixed32  +0.558334
  3.3.2.3.#2  fixed32  -0.094220
  3.3.2.3.#3  fixed32  +0.053970
  3.3.2.3.#4  fixed32  +0.822480
3.3.2.#3[1] message (20 bytes)    <- quaternion 2, byte-identical to 3 …
```

So interpolating sub-sample times is not a shortcut anyone settled for; it is
the only thing the data supports. The question is closed rather than open.

The two container scalars are new, and both are worth having. `djgyrofix info`
now prints them, because they are the first thing worth knowing about footage
somebody else recorded.

**Field 1 is a microsecond timestamp**, and it does not always agree with the
container:

| Clip | DJI's clock | Container DTS | Drift |
|---|---|---|---|
| RAW | 60.0020 Hz | 59.9401 Hz | +0.103%, +0.513 s over 497 s |
| VILLA2 | 59.9445 Hz | 59.9401 Hz | +0.007%, +0.017 s over 228 s |

RAW's metadata clock runs at a clean 60 Hz while its container is 59.94 — the
two disagree by half a second across the flight. This changes nothing about
detection: a 0.1% rate error scales every angular velocity by 0.1%, which is
nothing against a 60 °/s threshold, and Gyroflow reads the same container
timeline this tool does, so reported event times still land where the viewer
scrubs to. It is recorded because it is a measurable difference between two
clips from the same manufacturer, and because the clip it appears on is the one
with the reported artifacts. That may be a coincidence. One clip cannot say.

**Field 2 is a sample counter** that steps by exactly one, without exception,
through the entire body of both clips. That makes a dropped metadata sample
*identifiable* rather than inferred — everywhere else in this tool a drop has to
be guessed at from timestamps, which is the guess §5.3's plausibility gate was
tightened over twice. Both clips end with the same two-step oddity in their
final two samples (`…297 → …299 → …299`), so a disturbed trailing pair is normal
and a gap in the body would not be.

Nothing in detection uses the counter yet. There is no footage here with a real
drop in it, and adding a detection path that no available clip exercises would
be a guess dressed as a feature. It is surfaced in `info` so that incoming
footage is checked for one, and it is written down here so the option is not
rediscovered.

## An Osmo, and what the upstream verdict cannot tell you

`OSM_20260808192827_0003_D.MP4` is 346 s from an Osmo with visible vibration in
the footage that is not the result of sharp flying. It needed no new support:
its metadata track sits behind a `CAM meta` handler rather than `DJI meta`, but
the `djmd` sample entry selects it and the same `wm169` path finds 344,493
quaternions. Two differences from the air-unit clips are worth recording.

**It does not duplicate**, because it is a 29.97 fps clip. The O4 clips are
59.94 fps and store 1978 Hz of which exactly half is padding; this one stores 989
Hz of unique attitude, `duplicate_share` 0.00. Both cameras are filling about
33.3 slots per video frame from an IMU near 1000 Hz — see the frame-rate table
above, which this clip is what established. Its Nyquist limit is therefore 494.5
Hz where theirs is 989, and that matters for the band found below.

**It is the first clip here to come back `upstream`**, and by a wide margin. Its
noise floor is 113.7 °/s typical against the artifact clip's 3.4, with 87% of
the footage at or above 45 °/s. One event is found in 346 s. The report says the
metadata is not what is wrong, and recommends the mount and the tune.

That recommendation is right. The reasoning behind it is thinner than it looks.

### The roughness is coherent, not a floor

Taking the angular rate between consecutive stored orientations and running a
Hann-windowed DFT over the roughest and quietest 4096-sample windows:

| band | quietest window | roughest window |
|---|---|---|
| 1–8 Hz | 0.10 | 0.78 |
| **8–16 Hz** | 0.10 | **2.45** |
| 16–63 Hz | 0.08 | 0.42 |
| 63–400 Hz | 0.06 | 0.15 |
| 430–450 Hz | 0.04 | 0.35 |
| **450–480 Hz** | 0.05 | **1.38** |
| 480–494 Hz | 0.04 | 0.30 |

The quiet window is flat — 8.3 °/s rms, no structure. The rough window is 138.9
°/s rms and has two distinct components: a dominant one at **9 Hz** and a
narrow band at **450–480 Hz**, each about an order of magnitude above the
63–400 Hz floor between them.

Neither is broadband sensor noise. Noise has no band to find, and a derivative's
noise tilt rises smoothly to Nyquist rather than peaking at 465 Hz and falling
away by 480. Two coherent oscillations driving the camera is what a mechanical
resonance looks like, which is why the mount-and-tune advice lands even though
the tool reached it by calling the floor high.

Whether 450–480 Hz is the real frequency or an alias of 509–539 Hz folding back
across the 494.5 Hz Nyquist limit, this data cannot say. Distinguishing them
needs a faster reference than the metadata provides.

### Why this tool cannot help, in its own terms

The detector's model is a transient excursion from a local trend. A sustained
9 Hz oscillation is not a transient — over a 12 s baseline window it *is* the
trend, so the rolling threshold rises to meet it (341.1 °/s here) and detection
goes quiet exactly where the footage is worst. The report already warns that a
short event list is not a clean bill of health on a rough clip. This clip is
what that warning was written for.

Attenuating a coherent 9 Hz component is a filtering problem, not a
transient-repair one, and Gyroflow's own low-pass is the right instrument for
it. Nothing here should try to grow into that.

### What the diagnosis could say and does not

`upstream` currently reports one number, the noise floor, and gives one remedy.
It cannot separate a genuinely noisy sensor from a mount resonance, and those
have different fixes. The measurement above took a DFT over two windows to
answer, and it produced something a pilot can act on — *9 Hz, and something near
465 Hz* — where "your noise floor is 113.7 °/s" does not point at anything.
Reporting the dominant frequencies alongside an `upstream` verdict would make it
a much better diagnostic. It is not implemented; one clip is not enough to
choose thresholds for it.

## Cutting a spike instead of smoothing it

The reasonable intuition about a transient is that it should be *removed*, not
attenuated: the excursion is not real, so cut it back rather than filter it. Four
ways to do that were measured against the same 81 real events, scored on the
angular-acceleration residual and on the largest single-sample orientation step
each one leaves behind.

| method | clip residual | in-region | largest step |
|---|---|---|---|
| unchanged | — | — | 1.85° |
| blur (legacy) | 6.6% | 95.9% | 0.86° |
| slerp runs (shipped default) | 4.4% | 75.3% | 0.86° |
| **hold at last good value** | **10.4%** | **100.0%** | **156.80°** |
| limit deviation to 0.05° | −84.7% | 87.3% | 0.52° |
| limit deviation to 0.10° | −29.1% | 84.1% | 0.61° |
| limit deviation to 0.20° | −5.3% | 78.4% | 0.81° |
| limit deviation to 0.50° | +0.7% | 64.8% | 0.85° |

### The hold result is a hole in the metric, not a discovery

Freezing orientation at the last good sample scores better than every shipped
method on both figures — the only 100% in-region result there has ever been —
while introducing a **156.8° instantaneous jump**. The events are transients
superimposed on real flight, and the flight does not pause for them: holding for
up to 1.1 s while the aircraft genuinely rotates, then resuming, produces exactly
that step.

The residual metric cannot see it. A frozen orientation has no angular
acceleration inside the region, which is what the in-region figure measures, and
the step lands on the boundary. **A correction can therefore score perfectly and
destroy the footage.** That is a property of the harness rather than of this
idea, and it would have blessed any future method with the same shape, so
continuity is now asserted separately from quality by
`TestCorrectionNeverIntroducesALargerStep`.

### Clipping generates the thing it is trying to remove

The limiter is the more careful version of the intuition: below the cap it is the
identity, above it the excursion is truncated back along the arc to the trend, so
unlike a hold it cannot leave a step. It still makes the clip-wide residual
*worse*, monotonically with how hard it clips — −84.7% at a 0.05° cap.

The reason is structural. Clipping is nonlinear: it puts a corner in the
orientation path wherever the signal crosses the cap. Angular acceleration is the
second derivative, so a corner is an impulse, in the same way that clipping a
waveform generates harmonics. The defect being removed *is* a derivative
discontinuity, and truncation manufactures more of them. A soft knee avoids the
corner, but a soft-knee limiter with a high enough ratio is a filter — which is
what the blur already is.

### There is no headroom in cutting more, either

Raising the longest run that run-repair may replace, which is the same intuition
expressed inside the architecture that cannot leave a step:

| cap | runs interpolated | too long | in-region |
|---|---|---|---|
| 30 ms (shipped) | 1152 | 3 | 75.3% |
| 60 ms | 1154 | 1 | 75.1% |
| 120 ms | 1155 | 0 | 75.2% |
| 250 ms | 1155 | 0 | 75.2% |

Three runs in the whole clip were ever refused for length. The cap is not what
limits the result, and the residual that survives is not spike-shaped: located
earlier in this document, it sits as often in the middle of a corrected region as
at its edges. Cutting is the wrong instrument for a residual that is distributed.

### One result that is not about cutting

The blur scores better than the shipped default here — 95.9% against 75.3%
in-region, 6.6% against 4.4% clip-wide — on the clip the default was chosen for.
That is not on its own an argument for changing back. Run-repair replaces only
the samples that are out of trend where the blur smooths whole events including
the genuine motion inside them, and neither the residual metric nor anything else
here measures the motion a correction wrongly removed. It is recorded because it
is a real number pointing the other way from a decision already taken, and
because the labelled corpus is what would settle it.

## Clean metadata that stabilizes badly anyway

`DJI_20260512144600_0012_D.MP4` is 66 s from an O4 on firmware 01.00.03.00. Its
owner reports that it jerks after stabilization no matter what correction is
applied, including `--profile aggressive --sensitivity 1.5 --smoothing-ms 200`.

The tool called it patchable, and by its own measure it was right: 58 events,
10.5% affected, and a residual floor of **6.7 °/s** — cleaner in proportion than
anything else measured here, since the aircraft is turning at rates where 6.7 is
1.3% of the signal. There is nothing much wrong with the record.

The problem is what was recorded.

| clip | rate p50 | p90 | p99 | above 333 °/s | skew at 15 ms |
|---|---|---|---|---|---|
| **0012_D (jerks)** | 50 | 333 | 854 | **10.0%** | **12.8°** |
| RAW (real artifacts) | 35 | 166 | 430 | 2.2% | 6.4° |
| VILLA2 (owner: normal) | 10 | 21 | 37 | 0.006% | 0.6° |

A tenth of this clip is flown faster than 333 °/s. Rolling-shutter skew is rate
times readout time, so at a nominal 15 ms line-scan the frame is distorted by
around 12.8° *internally*, before stabilization sees it, and motion blur has
smeared it as well. Neither is in the attitude track. No amount of attitude
correction reaches either one, and the spectrum agrees: the rough windows here
are 50 times stronger at 1–4 Hz than anywhere above, which is the aircraft
flying, not an artifact.

Two hypotheses were checked and discarded on the way. The metadata track steps
**one-for-one with the video track** — 3945 samples against 3945 frames, same
timescale — so the difference between DJI's own clock and the container cannot
put attitude out of step with frames, whatever else it means. And the padding is
the ordinary 50% for a 59.94 fps clip.

### What the tool got wrong

It said "this is what djgyrofix is for", and then suggested a stronger detector
when the correction did not visibly help. Both were wrong in the same way:
nothing in the verdict looked at how hard the aircraft was actually turning. The
residual figures describe how faithful the record is; they cannot describe
whether stabilizing that record was ever going to work.

The report now measures absolute rotation rate beside the residual, and where a
meaningful share of a clip is past the skew threshold it says so in the verdict
block and withholds every detector suggestion — because each of them would spend
its effect smoothing real motion, which is how a clip gets worse the harder it is
corrected. That is what the settings above were doing.

The 5% threshold that decides this is set from three clips and separates the one
that behaves this way from the one with genuine artifacts. It is a judgement
call and wants a corpus.

## Scope of the evidence

This is three real clips — two O4 air units and one Osmo — plus generated
fixtures designed to isolate clean vector changes, sustained ringing, true short
corruption, continuous over-rate motion and correction composition. It validates this failure mode and
the full patch/verify/revert path; it is not yet a broad labelled corpus across
air-unit models, mounts and flight styles. New footage should still be scanned
first, and early tests should use a copy or `--out`.
