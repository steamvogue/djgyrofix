# Changelog

Notable changes to djgyrofix. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/steamvogue/djgyrofix/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/steamvogue/djgyrofix/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/steamvogue/djgyrofix/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/steamvogue/djgyrofix/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/steamvogue/djgyrofix/releases/tag/v0.1.0
