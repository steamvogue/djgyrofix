# Changelog

Notable changes to djgyrofix. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.6.0] — 2026-09-02

### Added

- **`--repair runs`**, an outlier-replacement correction proposed by a user of
  the tool. Instead of low-passing a whole detected event, it finds the
  supra-threshold runs inside it and interpolates each along the arc between its
  neighbours, leaving every sample outside a run byte-identical.

  The measurement behind it: on the real clip the per-sample residual crosses
  four times its median in 3,145 runs whose median length is 4.04 ms and whose
  p90 is 25.28 ms, inside events spanning hundreds of milliseconds. The blur was
  smoothing 300 ms of genuine motion to remove 4 ms of overshoot. On a fixture
  built from short excursions the blur touches 270 samples where run-repair
  touches 30; on the real clip it patches 134,337 quaternions against the blur's
  165,207 for the same detection.

  Two guards bound the risk, which is the mirror of the benefit — these runs
  cluster on sharp movements, where an interpolation is most likely to invent an
  orientation. Runs over 30 ms are never replaced, and a run is only replaced
  when its endpoints still match the surrounding trend, so a deviation that
  departs and stays departed is treated as real motion. Opt-in, because
  fabricating orientation is the one mistake here that cannot be seen in the
  output.

### Changed

- The README is rewritten for someone who wants their footage fixed rather than
  someone auditing the detector: 880 lines down to 242, download link second on
  the page, internals moved to the docs that already covered them.
- `docs/USAGE.md` is new — every flag with its real default, the recipes, undo,
  exit codes, and which knobs actually affect real footage.
- `--baseline-window` no longer claims a 5s default in its help text; it comes
  from `--style`.

## [0.5.1] — 2026-09-01

### Fixed

- **The reported residual reduction was almost entirely measuring DJI's
  duplicated samples.** The metric is the reference's median angular
  acceleration, differenced across stored samples, and on the real clip 99.7% of
  it was the duplication stair-step (2469.5 as stored, 6.8 with the repeats
  dropped). A box blur removes that stair-step whether or not it removes any
  jitter, so a run could report 91.6% and look identical in Gyroflow.

  Reported figures are now measured on distinct orientations, with one index
  mask taken from the before-series and applied to both so the two share a time
  grid. On the real clip that reads 91.4% inside the corrected regions and 9.8%
  clip-wide; on a clip considered normal, 99.3% and 0.1%.
  `correct.AngularAccelerationScore` keeps the reference's exact behaviour,
  because the golden fixtures pin it, and golden parity still passes 72 of 72.

## [0.5.0] — 2026-09-01

A viewer reported that a patched file changed nothing in Gyroflow. Checking
Gyroflow's own parser against both files showed it reads exactly the field this
tool patches, and sees the patch. It also showed why the patch was aimed at the
wrong thing.

### Fixed

- **Angular velocity is differenced against the last distinct orientation.**
  DJI stores every fused attitude twice: measured across two real O4 clips,
  exactly 50.00% of stored quaternions are byte-identical to their predecessor,
  in runs of exactly two at a fixed parity. The track presents 1978 Hz and
  carries 989 Hz. Differencing consecutive stored samples made every other
  velocity exactly zero and doubled the rest — a square wave at Nyquist whose
  amplitude scales with rotation rate, so it was largest exactly where the
  detector was looking.

  On the clip with real artifacts it accounted for three quarters of the
  whole-clip residual (118.4 °/s RMS → 31.6) and inflated the apparent noise
  floor tenfold (37.9 °/s → 3.4), dragging every threshold up with it. Real
  transients were hiding inside phantom noise several times their own size. On
  a clip its owner considers entirely normal, reported events fall from two to
  one over 228 seconds, and the apparent floor from 10.5 °/s to 2.0.

  The collapse is decided per file from the measured duplicate share, never
  assumed: a frozen telemetry dropout is locally the same shape, and collapsing
  one would erase the signature that makes it reconstructable.
- **The `upstream` noise level is recalibrated to 45 °/s**, from 90. The
  previous value was derived from an apparent floor of 39 °/s that was roughly
  92% sampling artifact. Real anchors are now 2.0 and 3.4 °/s.

### Added

- `scan` reports an oversampled stored rate under the baseline line, and
  `duplicate_share` in the JSON report.

### Note on `--style`

The flight-style presets added in 0.4.0 were treating this symptom. With the
duplication handled, the threshold on both real clips sits at the absolute
`--floor-dps` everywhere and the style makes no measurable difference on either.
The flag still does what it documents and still matters where the σ term drives
the threshold, but it is no longer the answer to jitter surviving correction.

## [0.4.0] — 2026-09-01

Both changes here came from a viewer reporting sharp jitter that survived
correction at 1:19–1:21 of the real clip. The jitter was real, the cause was
under-detection, and the reported score had said the run went well.

### Added

- **`--style cinematic | normal | freestyle`**, which sets the rolling baseline
  half-width to ±5 s, ±12 s or ±20 s and nothing else. It is a separate axis
  from `--profile`: profile is how strict detection is, style is the timescale
  it works over. `--baseline-window` still takes a duration and overrides it.
- **Clip-wide residual reduction is reported alongside the in-region figure**,
  clip-wide first. The in-region figure cannot fall where detection never
  looked, so it reads best exactly when under-detection is the problem: the
  real clip reported 91.6% while a burst it had covered 12% of was still
  plainly visible. The same run measures 16.1% clip-wide.

### Changed

- **The default baseline window is now ±12 s, up from ±5 s.** A two-second
  burst inside a ten-second window supplies a fifth of the samples its own
  threshold is computed from, so it raises the bar meant to catch it and hides.
  On the real clip a burst at 79–81 s sat at 165–235 °/s against a 605 °/s
  threshold, and 12% of it was flagged; at ±20 s that becomes 46%. Pass
  `--style cinematic` for the previous behaviour, which reproduces the old
  results exactly.
- The rolling baseline window is capped at a quarter of the clip duration. A
  half-width reaching past half the footage is not rolling any more, and on a
  thirty-second clip a ±12 s window flattened localized roughness back into the
  clip's own level — the failure the rolling baseline was introduced to fix.
- `upstream` is now reached at 20% of a clip above the noise level, down from
  25%. `--max-affected` refuses at 15%, so a noise floor covering more than
  that cannot be corrected even in principle.

## [0.3.2] — 2026-09-01

Two defects in the 0.3.0 diagnosis, both found by running it on real footage.

### Fixed

- **The `upstream` verdict fired on repairable footage.** The level separating
  background noise from a defect was derived from `--floor-dps`, and was
  calibrated against synthetic fixtures whose clean case idles at 0.6 °/s —
  a 200× gap from the rough case, wide enough that any threshold between them
  looked correct. Real FPV footage carries a far higher residual floor. The
  8m17s clip in [the findings](docs/FINDINGS.md) measures 39.2 °/s typical and
  66.7 °/s at p90 and is demonstrably repairable — 6.17% flagged, a 91.6%
  residual reduction, a rescan that comes back empty — and it was being called
  an airframe problem. Under `--auto` that refused to patch it.

  The level is now an absolute 90 °/s and no longer moves with `--floor-dps`,
  `--profile` or `--sensitivity`. How much an airframe resonates is a property
  of the aircraft, not of how wide a search was asked for; the same footage
  changing diagnosis because only the search changed was wrong in principle as
  well as in practice. A regression test pins the real clip's measurements on
  one side and a resonating clip on the other.
- **A wrapped line could end on a single short word**, which read as a
  rendering fault rather than as a wrap. The wrapper now reflows the line above
  to absorb a stub, and the `upstream` headline is short enough not to wrap at
  all.

## [0.3.1] — 2026-09-01

Tooling only: v0.3.0 shipped correct binaries from a tree that did not pass its
own lint gate. This release is that tree, made clean.

### Fixed

- A redundant initialization in `advise.Evaluate` that `golangci-lint`
  (`wastedassign`) rejects. No behaviour change; the v0.3.0 binaries are
  unaffected.
- `make lint` now runs `golangci-lint` when it is installed, rather than only
  `gofmt` and `go vet`. A local gate that checks less than CI does is a gate
  that hands you a red build — which is exactly what happened on v0.3.0.

## [0.3.0] — 2026-09-01

The report now interprets its own numbers. A scan ends in a verdict a pilot can
act on rather than a baseline and a threshold that only mean something to
whoever wrote the detector.

### Added

- **A verdict on every automatic scan and fix.** The report now ends in a
  diagnosis block: one of `patch`, `upstream`, `review` or `clean`, the
  measurements behind it, the flags worth trying with the measurement that
  suggests each, and the command to run. Previously the report printed the
  numbers and left every interpretation to the reader.
- **Noise-floor percentiles** (`p10`/`p50`/`p90`) and the share of a clip
  sitting above the noisy level, exposed on `detect.Result` and in the JSON
  report under `noise`. The reported baseline is a clip median, which cannot
  distinguish clean footage from footage that is clean for most of its length
  and resonating for the rest; the percentile spread can.
- **The `upstream` verdict**, which is the one this was built for. The rolling
  Hampel threshold rises with the local noise floor, so a badly mounted stretch
  raises its own bar and stops producing events — a resonating clip could
  report a short, reassuring event list. The verdict now says that a quiet
  event list over a high noise floor is a symptom rather than a clean bill of
  health.
- **`--auto`,** an autopilot for profile selection on `scan` and `fix`. It steps
  one profile stricter when detection exceeds `--max-affected`, one profile
  looser when nothing was kept but several events scored just under
  `--min-severity`, and refuses outright when the noise floor makes the clip an
  airframe problem. It is rule-based rather than an optimizer, because
  minimising a residual score has a degenerate answer: smooth everything. Every
  step and its reason are recorded on the report, explicit flags still outrank
  the profile it lands on, and a refusal yields to `--force`.
- Predicted residual reduction is now printed on a dry run, labelled as measured
  inside the corrected regions only. It was already computed and only reported
  after a patch had been applied.

### Fixed

- The `Try it` session in the README predated the 0.2.0 detector and reported
  three events, 1.33 s affected and 331 patched quaternions where the shipped
  binary reports four, 1.49 s and 418.

## [0.2.0] — 2026-09-01

This release replaces the first automatic-correction model after validating it
end to end on the supplied 6.57 GB DJI clip. The observed problem is a fast but
comparatively low-frequency, out-of-sync attitude response around sharp vector
changes, not simply generic high-frequency gyro noise.

### Added

- **Bounded correction rescans.** Automatic correction now rescans its
  float32-quantized result for at most three passes. Residual events near an
  authorized region can be corrected again; newly exposed smoothing regions
  can join only while their union remains below `--max-affected`. Newly detected
  dropouts are never reconstructed automatically during a later pass.
- `scan` performs the same bounded analysis as `fix`, so its event list and
  dry-run write estimate predict the correction that will actually be applied.
- Synthetic `vector-change` and `vector-jitter` fixtures cover clean sharp
  changes separately from the damped out-of-sync response.
- [Real-footage findings](https://github.com/steamvogue/djgyrofix/blob/v0.2.0/docs/FINDINGS.md)
  record the rejected hypotheses,
  implementation consequences, measured full-clip result and limits of the
  evidence.
- Every push to `main` publishes 30-day Windows amd64 and arm64 artifacts,
  including checksums and documentation. Tagged releases still publish all six
  Linux, macOS and Windows targets.
- The former `main` implementation is preserved on the `study` branch for
  reproducible comparison; `main` and `rebirth` carry the replacement.

### Changed

- **Detection targets transient deviations rather than only high-frequency
  content.** Angular velocity is now compared with a ±60 ms local trend instead
  of ±12 ms. The residual exposes overshoot and damped ringing while the
  motion-ratio gate keeps intentional rapid movement distinct.
- **Confirmed events receive a stable correction core.** Detection confidence
  supplies a non-zero floor across the complete event, peak excess may raise the
  whole core to full correction, and fixed exterior shoulders taper smoothly.
  This removes the old near-threshold under-correction and avoids introducing
  blend steps at 10 ms bin boundaries.
- Bridges are applied to one working quaternion series before that series is
  smoothed, so overlapping bridge and jitter corrections share corrected
  boundaries instead of composing against stale input.
- Reports now say `transient residual` rather than `high-frequency residual`,
  matching what the score actually measures.

### Fixed

- Over-full-scale transition pairs are bridgeable only when the second edge is
  opposing, returns near the pre-entry orientation and is followed by a settled
  continuation. Genuine impacts and continuous oscillation are no longer
  reconstructed as telemetry dropouts.
- Event end times are clamped to the last quaternion timestamp. A report can
  therefore be fed back through `--ranges` even when an event reaches the end of
  the track.
- A bridge inside a smoothing event no longer interpolates from the original
  unsmoothed neighbours, which could create a fresh boundary discontinuity.
- Exported scorer documentation now passes the same `revive` lint gate used by
  release CI.

### Performance

- Before/after angular acceleration is normalized and differentiated once per
  track. Event scores query the prepared indexed series instead of traversing
  all quaternions for every event, removing the events × quaternions scaling
  cost seen on long clips.

### Validation

- The supplied 497.26-second clip contains 993,523 quaternions at 1978 Hz. The
  bounded analysis reports 85 events (73 jitter, 12 impact, 0 dropout), including
  three regions exposed during correction, and affects 30.6828375 seconds
  (6.1703%).
- Correction changes 102,824 quaternions in 3,164 metadata samples through
  411,268 four-byte writes and reduces the transient score by 91.598%.
- A rescan of the patched copy reports no events. Verification passes every
  range, the metadata digest and the unchanged 6,570,736,456-byte file size;
  revert compares byte-identical with the original.
- CI covers Linux, macOS and Windows tests and race tests, all six cross-builds,
  lint, both parser fuzz targets and byte-for-byte golden parity with the Python
  reference.

## [0.1.1] — 2026-09-01

### Changed

- **The version is resolved from the build instead of a source constant.** A
  hardcoded version and a git tag are two places holding the same number, and
  they drift the first time one is bumped without the other — leaving a binary
  that misreports itself in every patch journal it writes. Release builds are
  now stamped from the tag, `go install` picks up the module version, and a
  build from a working tree reports its commit rather than claiming to be a
  release.
- The release workflow asserts that the binary reports the tag it was built
  from, and refuses to publish if it does not.

### Fixed

- The release workflow now verifies each cross-compiled binary is non-empty
  before archiving it. A compile that emitted nothing would previously have
  produced a publishable archive containing no binary.

## [0.1.0] — 2026-09-01

First release. A dependency-free Go rewrite of
[`kim2160/DJIGyroFix`](https://github.com/kim2160/DJIGyroFix) v0.92, with
byte-for-byte output parity against it in manual-range mode.

### Added

- **`scan`, `fix`, `revert`, `verify` and `info` subcommands.** `fix` is a dry
  run unless `--apply` is given.
- **In-place patching with exact revert.** The patch is size-preserving, so a
  sidecar journal recording every four-byte write is enough to restore the
  original file bit for bit — no need to duplicate a multi-gigabyte video to
  enable undo. Measured on a real 6.5 GB clip: 11 s to patch, 1.3 s to revert,
  28 MB of journal.
  The upstream tool has neither in-place patching nor revert.
- **Automatic detection**, replacing the original's hand-picked time ranges.
  Angular-velocity residual as the discriminator, a sliding Hampel window for
  the threshold, and a physical plausibility gate deciding what may be
  reconstructed rather than merely smoothed.
- **Four event classes** — `dropout`, `impact`, `jitter` and `motion` — with
  `motion` deliberately left alone. A whip-pan is smooth fast rotation and
  smoothing it would degrade footage that was fine.
- **SLERP bridging for dropouts**, gated on the plausibility test. Blurring a
  dropout destroys the surrounding motion; interpolating between the last good
  orientation and the first good one after removes only the glitch.
- **A continuous correction weight envelope**, which deletes the original's
  edge-correction block entirely: the weight falls to zero at every event edge
  by construction, so there is no discontinuity left to patch over.
- **Per-event smoothing windows** derived from event duration, replacing one
  global 180 ms that is wrong for both a 40 ms impact and a two-second jitter run.
- **`--max-affected`**, refusing to patch when detection flags more than 15% of
  a clip — the signature of a bad baseline or genuinely rough footage.
- **`--backup copy`** using APFS `clonefile` and Linux `copy_file_range`, so a
  full backup costs no space on a copy-on-write filesystem.
- **`text`, `json`, `edl` and `csv` output**, with `--ranges` accepting the
  reported ranges back so a human can stay in the loop over a batch.
- **`--jobs` for parallel batch processing**, with output in argument order.
- **Golden parity harness.** With detection disabled, output is byte-identical
  to `gyrofix.cli` across 72 cases spanning three metadata variants.

### Not ported

- The desktop GUI (`ui.py`) and its translations (`i18n.py`).
- Writing a full copy of the video for every edit. `--out` keeps that behaviour
  where it is wanted.

[Unreleased]: https://github.com/steamvogue/djgyrofix/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/steamvogue/djgyrofix/compare/v0.5.1...v0.6.0
[0.5.1]: https://github.com/steamvogue/djgyrofix/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/steamvogue/djgyrofix/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/steamvogue/djgyrofix/compare/v0.3.2...v0.4.0
[0.3.2]: https://github.com/steamvogue/djgyrofix/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/steamvogue/djgyrofix/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/steamvogue/djgyrofix/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/steamvogue/djgyrofix/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/steamvogue/djgyrofix/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/steamvogue/djgyrofix/releases/tag/v0.1.0
