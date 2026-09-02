# Changelog

Notable changes to djgyrofix. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.10.0] — 2026-09-02

### Changed

- **Text reports now end with a decision, not a loose collection of flags.** A
  patchable scan or dry run gives the apply command, asks the user to preview
  the listed times in Gyroflow, explicitly says to stop when stabilization is
  smooth, and only recommends a stronger retry when the visible result matches
  the measured warning. In-place retries include the revert command; `--out`
  retries replace only the derived copy; a no-journal patch is never told to
  compound correction on the modified source.
- Suggested commands preserve the scan's detection and correction settings and
  replace only the flag being recommended. JSON advice now also carries each
  suggestion's condition and exact command, the preview target, the safe revert
  command when one exists, and the report's originating operation.
- Residual-reduction figures now explain why a strong in-region result can sit
  beside a small clip-wide number. Run-repair output distinguishes interpolation
  from the bounded smoothing used for long and motion-like runs, instead of
  implying that every non-interpolated run was simply left untouched.
- **Release publishing now waits for the complete CI matrix.** The tag workflow
  calls the same reusable workflow as normal CI, covering tests and race tests
  on Linux, macOS and Windows, lint, all six cross-builds, parser fuzzing and
  golden parity before any archive is published.

### Fixed

- **A simultaneous control input could hide an independently significant
  across-axis artifact.** The 0.9.1 event-level guard treated every event below
  a fixed across/total residual ratio as intentional motion. A genuine wobble
  could therefore be discarded when a stronger along-axis acceleration occurred
  at the same time. The across component now gets the detector's normal
  admission test first; when it clears that bar on its own, the event remains
  actionable.
- Added regression coverage proving that a positive telemetry packet gap does
  not create a bridge event, while duplicate and regressing timestamps still
  fail the plausibility gate.

## [0.9.1] — 2026-09-02

### Fixed

- **Along-axis control rate changes were falsely classified as transient spikes.**
  Pass 1 event detection previously evaluated only the total residual magnitude,
  computing the across-axis decomposition after detection had already finished.
  Rapid along-axis maneuvers (such as abrupt yaw or pitch control inputs) could
  exceed threshold and get classified as jitter or impact events. `classify()`
  now evaluates the across-axis residual ratio, correctly identifying along-axis
  rate changes as intentional `motion` (`ActionNone`).
- **`localPeakCount` missed leading and trailing boundary peaks.** Candidate
  selection was restricted to interior bins ($1 \le index \le len-2$), omitting
  the leading edge (index 0) and trailing edge (index $len-1$). Abrupt impacts
  and vibration bursts with initial peaks were undercounted, which could lead to
  jitter events being misclassified as impacts. Boundary bins are now evaluated.
- **Telemetry packet drops marked subsequent valid samples as corrupt.** In
  `plausibilityGate`, any step $> 3 \times interval$ marked sample $i$ as
  implausible, causing `bridge()` to overwrite valid incoming orientations. It
  now flags only non-monotonic timestamp regressions ($t_{i+1} \le t_i$),
  leaving valid samples across transmission drops intact.

## [0.9.0] — 2026-09-02

### Fixed

- **`--ranges` silently ignored `--repair`.** Manual-range mode has always
  corrected by blur — it is the path held to byte-for-byte parity with the
  Python reference, which has no notion of run-repair — but it accepted
  `--repair runs` and discarded it, and wrote a journal with no `repair` key at
  all. Harmless while `blur` was the default and the two agreed; since 0.8.0 it
  meant every manual-range fix quietly used a mode other than the documented
  default. The mode that ran is now recorded in the journal, asking for the
  other one warns, and `docs/USAGE.md` says so.
- **An empty `--repair` was accepted and then misreported.** `--repair=""` is
  taken as "use the default", as an empty `--format` already is, but the value
  was carried into the journal verbatim while run-repair ran — a provenance
  record that did not name the correction that produced the patch. It now
  resolves to `runs` before anything reads it.

## [0.8.1] — 2026-09-02

Documentation only; the binary is identical to 0.8.0.

### Fixed

- **The artifact model in the docs was narrower than the evidence.** It
  described "a fast but comparatively low-frequency, out-of-sync attitude
  response around sharp motion-vector changes", from one clip measured before
  the sample duplication was understood. That duplication inflated the residual
  in proportion to rotation rate, which put the apparent artifact where the
  aircraft was turning hardest and made the association with vector changes look
  stronger than it is. Community reports of the same symptom span frames, builds
  and flying styles and describe vibration sensitivity, not turns.

  Now described as what it is: frame vibration recorded into the attitude track,
  surviving as brief excursions from the local trend — median 4 ms, spread
  across the clip rather than confined to manoeuvres. No code change; the
  detector already worked on the residual rather than on any assumption about
  when the deviation occurs.

## [0.8.0] — 2026-09-02

### Changed

- **`--repair runs` is now the default.** Correcting an event means replacing
  the samples inside it that are out of trend, rather than low-passing the whole
  span. On the real clip that is 6,198 runs replaced while touching 91,241
  quaternions, against 165,207 for the blur at the same detection settings — a
  third fewer samples modified, and the genuine motion between the runs left
  exactly as recorded.

  `--repair blur` restores the previous behaviour exactly, and remains the path
  held to byte-for-byte parity with the Python reference.

  This moves a real risk into the default path, so it is worth stating plainly:
  replacing a run interpolates orientation, and on a run that is genuinely rapid
  motion rather than an excursion, that invents attitude the aircraft never
  held. Three things bound it — runs over 30 ms are never replaced, a run is
  only replaced when its endpoints still match the surrounding trend, and
  selection uses the residual across the rotation axis so a rate change about
  the axis already being turned is not mistaken for a wobble. It is still a
  newer path than the blur, and `revert` is exact if it goes wrong.

## [0.7.1] — 2026-09-02

Documentation only; the binary is identical to 0.7.0.

### Changed

- `docs/USAGE.md` gains the questions people actually arrive with: an
  escalation ladder for footage that still shakes after a repair, the
  compare-by-eye workflow with `--out`, how to read the two improvement figures
  as a pair, and a table of which flag arrived in which release — because an
  older binary answers an unknown flag by printing its usage, which reads as a
  syntax error and is not one.

## [0.7.0] — 2026-09-02

### Changed

- **Run-repair selects runs by the residual across the rotation axis**, not by
  its plain magnitude. Suggested by a user watching an aggressive manoeuvre
  where the correction cut into a real exit rotation.

  Turning faster or slower than the local trend about the axis you are already
  turning about is flying; the axis itself moving is the artifact. The plain
  magnitude gives one number for both. Measured across one manoeuvre on the real
  clip, a 366 °/s exit rotation reads 29% across-axis where the wobble 400 ms
  later reads 92% — the same second, a threefold separation on a quantity the
  detector was not computing.

  The effect on the real clip: 6,198 runs replaced against 4,901, while touching
  91,241 quaternions against 134,337. More of the artifact found, a third fewer
  samples modified, and runs too long to interpolate fall from 400 to 117.
  Inside the reported wobble it replaces 104 samples where the old measure
  replaced 133, leaving the along-axis rate variation alone.

  `--repair blur` is unaffected, and golden parity still passes 72 of 72.

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

[Unreleased]: https://github.com/steamvogue/djgyrofix/compare/v0.10.0...HEAD
[0.10.0]: https://github.com/steamvogue/djgyrofix/compare/v0.9.1...v0.10.0
[0.9.1]: https://github.com/steamvogue/djgyrofix/compare/v0.9.0...v0.9.1
[0.9.0]: https://github.com/steamvogue/djgyrofix/compare/v0.8.1...v0.9.0
[0.8.1]: https://github.com/steamvogue/djgyrofix/compare/v0.8.0...v0.8.1
[0.8.0]: https://github.com/steamvogue/djgyrofix/compare/v0.7.1...v0.8.0
[0.7.1]: https://github.com/steamvogue/djgyrofix/compare/v0.7.0...v0.7.1
[0.7.0]: https://github.com/steamvogue/djgyrofix/compare/v0.6.0...v0.7.0
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
