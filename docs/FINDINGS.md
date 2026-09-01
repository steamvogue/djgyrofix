# Real-footage findings

This document records what changed when djgyrofix was exercised end to end on
`testdata/DJI_20260705_RAW.MP4`. The clip itself is not committed: it is
6,570,736,456 bytes. The historical implementation is preserved on the `study`
branch; `main` contains the resulting detector and correction pipeline.

## The artifact model

The supplied failure is not well described as generic high-frequency gyro
noise. It is a fast but comparatively low-frequency, out-of-sync attitude
response around sharp motion-vector changes: overshoot and damped ringing remain
after subtracting a ±60 ms local angular-velocity trend.

That distinction matters. A plain angular-rate threshold cannot distinguish the
artifact from an intentional whip, impact or reversal. Detection therefore uses
the residual from the local trend, a rolling Hampel baseline and a motion-ratio
test. Plausible rapid motion may be smoothed when its residual shape warrants it,
but it is never reconstructed as missing orientation.

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

## Scope of the evidence

This is one long, real clip plus generated fixtures designed to isolate clean
vector changes, low-frequency ringing, true short corruption, continuous
over-rate motion and correction composition. It validates this failure mode and
the full patch/verify/revert path; it is not yet a broad labelled corpus across
air-unit models, mounts and flight styles. New footage should still be scanned
first, and early tests should use a copy or `--out`.
