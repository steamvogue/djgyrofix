# djgyrofix

[![CI](https://github.com/steamvogue/djgyrofix/actions/workflows/ci.yml/badge.svg)](https://github.com/steamvogue/djgyrofix/actions/workflows/ci.yml)
[![Main branch builds](https://github.com/steamvogue/djgyrofix/actions/workflows/development-build.yml/badge.svg)](https://github.com/steamvogue/djgyrofix/actions/workflows/development-build.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/steamvogue/djgyrofix.svg)](https://pkg.go.dev/github.com/steamvogue/djgyrofix)
[![Go Report Card](https://goreportcard.com/badge/github.com/steamvogue/djgyrofix)](https://goreportcard.com/report/github.com/steamvogue/djgyrofix)
[![License: GPL v3](https://img.shields.io/badge/license-GPL--3.0--or--later-blue.svg)](LICENSE)

**Your DJI footage looks fine until Gyroflow stabilizes it, and then it shakes.
This repairs the gyro data so it doesn't.**

[**Download →**](https://github.com/steamvogue/djgyrofix/releases/latest)
· [Command reference](docs/USAGE.md)
· [Changelog](CHANGELOG.md)

Windows, macOS and Linux. No install, no dependencies, one binary.

---

## Quick start

Three commands. Scan first — it opens the video read-only and can't hurt it.

```bash
djgyrofix scan DJI_0042.MP4          # what's wrong, and is this the right tool?
djgyrofix fix --apply DJI_0042.MP4   # repair it
djgyrofix revert DJI_0042.MP4        # put it back, byte for byte
```

Then stabilize as you always have. The metadata is fixed inside the original
file, so nothing about your Gyroflow workflow changes.

Prefer not to touch your original at all? Write a copy instead:

```bash
djgyrofix fix --apply --out fixed.MP4 DJI_0042.MP4
```

**`fix` writes nothing without `--apply`.** Run it bare and it tells you exactly
what it would change.

## The problem

If you fly FPV with a DJI air unit you may have hit this: the raw footage looks
fine, the goggles feed looks fine, and then RockSteady or Gyroflow turns it into
a twitching mess. Stabilization makes it *worse*.

It's a recurring complaint on [r/fpv][reddit] for the O4 Lite, and in May 2026
Oscar Liang traced a concrete cause on the O4 Pro: DJI
[quietly changed the gyro chip][oscar] in 2026-production units to a part already
known for being more sensitive to vibration. Mads Tech covers the same ground
[on video][madstech].

Here is the part that matters. **Gyroflow trusts the recorded gyro track
completely.** It reads those quaternions as ground truth for how far to
counter-rotate each frame. When the track carries a brief artifact — a dropout, a
corrupted sample, frame vibration recorded into the track — Gyroflow
counter-rotates against it with full confidence, and the correction is more
violent than the motion it was meant to remove. The stabilized clip looks worse
than the original not because stabilization failed, but because it succeeded
against bad input.

djgyrofix repairs those artifacts in the metadata, in place, and hands Gyroflow
something it can trust.

## What it can't do

**It cannot make a noisy IMU into a quiet one.** Nothing in software can. If your
camera is hard-mounted to a resonating frame, the fix is soft mounting and a
better tune — not this tool.

It also can't help with rolling-shutter wobble, which isn't in the metadata at
all. Gyroflow's own rolling-shutter correction handles that.

So it's also a diagnostic. Every scan ends in a plain verdict:

- **`patch`** — a handful of artifacts. This is what the tool is for.
- **`upstream`** — the noise floor itself is the problem. Go look at the mounting.
- **`review`** — too much of the clip is flagged to smooth safely.
- **`clean`** — nothing to repair. If it still shakes, try Gyroflow's
  Complementary integration or its low-pass filter.

Being told *"this is not a metadata problem"* in ten seconds is worth something
when the alternative is another evening of re-mounting a camera.

## What a scan looks like

```console
$ djgyrofix scan DJI_0042.MP4
DJI_0042.MP4  wm169  30 s  1500 samples  6000 quaternions @ 200.0 Hz
baseline 0.6 °/s   threshold 60.0 °/s (rolling)

  #  start        end          dur     type     sev  axes   peaks  action
  1  00:00:07.980 00:00:09.220 1.240s  jitter   10.0 X/Z    29     smooth
  2  00:00:14.980 00:00:15.050 0.070s  impact   10.0 Y      1      smooth
  3  00:00:24.920 00:00:25.100 0.180s  jitter   10.0 Y/Z    3      smooth
     note: contains samples that failed the plausibility gate
  4  00:00:25.000 00:00:25.005 0.005s  dropout  9.0  -      1      bridge

4 events, 1.49 s affected (4.98% of clip)

run-repair: interpolated 11 short artifact runs (42 quaternions)
  skipped interpolation for 10 long runs and 0 motion-like runs; their events
  used bounded smoothing instead

dry run: would patch 418 quaternions in 105 samples (6680 bytes)

diagnosis: 4 correctable events over 1.49 s (4.98% of the clip) — this is what
  djgyrofix is for
  noise floor 0.6 °/s typical, 0.6 °/s p90 — quiet enough that these events
  stand out as artifacts
  planned correction reduces transient residual 10.2% clip-wide, 100.0% inside
  the corrected regions
  read the in-region figure as the result where correction was aimed; the
  clip-wide figure also includes the 95.0% of footage outside the
  correction regions
next:
  1. Apply the planned correction:
     djgyrofix fix --apply DJI_0042.MP4
     If you prefer to leave corrupt orientation samples untouched, use
       --no-bridge instead — bridging is the only step that reconstructs
       orientation instead of smoothing existing samples:
       djgyrofix fix --apply --no-bridge DJI_0042.MP4
  2. Preview DJI_0042.MP4 in Gyroflow at the listed times.
     If stabilization is smooth, stop — you are done.
```

Four kinds of event, and only one of them gets reconstructed:

| Class | What it is | What happens |
|---|---|---|
| `dropout` | Telemetry corruption, valid data either side | Interpolated |
| `impact` | A brief single spike, physically plausible | Smoothed |
| `jitter` | Sustained shaking, several peaks | Smoothed |
| `motion` | You actually flew like that | **Left alone** |

## Common tasks

```bash
# See what a repair would change, without writing
djgyrofix fix DJI_0042.MP4

# If a residual warning matches visible twitching, restore and retry its advice
djgyrofix revert DJI_0042.MP4
djgyrofix fix --apply --sensitivity 1.3 DJI_0042.MP4

# Let it pick settings, and refuse footage it can't help
djgyrofix fix --auto --apply DJI_0042.MP4

# Smooth the whole event instead of replacing the bad samples (pre-0.8.0 behaviour)
djgyrofix fix --apply --repair blur DJI_0042.MP4

# A whole session
djgyrofix scan --format csv DJI_*.MP4 > events.csv
djgyrofix fix --apply --jobs 8 DJI_*.MP4
```

Every flag is in the [command reference](docs/USAGE.md), including a
[ladder to work down](docs/USAGE.md#if-it-still-shakes) when footage still
shakes after a repair.

## Your originals are safe

Every repair writes a small sidecar journal holding the original value of every
byte it touched, so `revert` restores bit-exact original state in milliseconds.
No multi-gigabyte copy needed.

- Only bytes inside the DJI metadata are ever modified. The video is untouched.
- The file size never changes, so the container stays valid.
- No ffmpeg, no re-encode, no remux.
- A file that already carries a journal is refused rather than patched twice.

**Still, a first run on footage you care about is worth doing on a copy.** This
is beta software operating on your originals.

## Install

Download a binary from the [releases page](https://github.com/steamvogue/djgyrofix/releases/latest)
— Linux, macOS and Windows, amd64 and arm64, with `SHA256SUMS` alongside.

```bash
tar xzf djgyrofix-*-linux-amd64.tar.gz
sudo install djgyrofix-*-linux-amd64/djgyrofix /usr/local/bin/
djgyrofix version
```

On macOS the binary is not notarized, so Gatekeeper blocks the first run. Either
right-click → Open, or `xattr -d com.apple.quarantine /usr/local/bin/djgyrofix`.

With Go 1.25 or later:

```bash
go install github.com/steamvogue/djgyrofix/cmd/djgyrofix@latest
```

From source: `git clone`, then `make` (fmt, vet, test, build).

Every push to `main` also publishes fresh Windows builds on the
[Main branch builds](https://github.com/steamvogue/djgyrofix/actions/workflows/development-build.yml)
page — 30-day development artifacts rather than releases.

## Which cameras work

Three DJI metadata layouts are understood: `wm169`, `wa530` and `oq101`. The
schema isn't public, so there's no list mapping models to layouts. Run:

```bash
djgyrofix info --all-variants YOUR_FILE.MP4
```

If one path finds a plausible number of quaternions, you're covered. If none do,
please [open an issue](https://github.com/steamvogue/djgyrofix/issues) with that
output — a new layout is a small change once someone has a file that needs it.

Fragmented MP4 is rejected outright rather than guessed at.

## Credit

The hard part of this — finding the quaternions inside an undocumented DJI
protobuf stream and patching them without breaking the container — was worked
out by **Minsu Kim** ([@kim2160][upstream]) in
[DJI Gyro Fix][upstream], a GPL-3.0 Python desktop tool. The field paths, the
variant sniffing, the offset-preserving scanner and the smoothing approach all
come from that work.

djgyrofix is a Go port and rework of it: a CLI instead of a GUI, automatic
detection instead of hand-picked time ranges, and in-place patching with exact
revert instead of writing a full copy. Its correction core is held to
byte-for-byte parity with the original — 72 of 72 cases — which is the clearest
way to say that the credit belongs upstream.

If you want a GUI, or you are on Windows or macOS and would rather not touch a
terminal, [use Minsu Kim's tool][upstream] — it is signed, notarized, and does
the same core job.

## How honest is this?

The tool is validated against two real DJI clips and a set of generated
fixtures, not a broad labelled corpus. Thresholds are judgement calls that have
already been wrong twice and corrected. What it measures, what it got wrong and
how that was found are written down rather than glossed:

- [Command reference](docs/USAGE.md) — every flag, with which ones actually bite
- [Measured findings](docs/FINDINGS.md) — real-footage results and the mistakes
- [Design notes](docs/DESIGN.md) — architecture, invariants, validation

Known limits worth stating plainly: variant sniffing is a heuristic that will
eventually guess wrong on some camera; sub-sample timing is interpolated because
no per-quaternion timestamp is known in the DJI schema; and the diagnosis rests
on a small number of real measurements, so if it calls your footage an airframe
problem and you disagree, that's worth an issue.

[reddit]: https://www.reddit.com/r/fpv/comments/1mzkd7v/dji_o4_lite_unwatchable_gyroflow_footage_heavy/
[oscar]: https://oscarliang.com/dji-o4-pro-gyro-stabilization-issue/
[madstech]: https://www.youtube.com/watch?v=YibY-87yFko
[upstream]: https://github.com/kim2160/DJIGyroFix

## Licence

GPL-3.0-or-later. This is a port and rework of
[`kim2160/DJIGyroFix`](https://github.com/kim2160/DJIGyroFix) v0.92 (GPL-3.0),
and therefore a derivative work. See [LICENSE](LICENSE).
