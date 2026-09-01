# Changelog

Notable changes to djgyrofix. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/steamvogue/djgyrofix/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/steamvogue/djgyrofix/releases/tag/v0.1.0
