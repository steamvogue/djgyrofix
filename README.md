# djgyrofix

[![CI](https://github.com/steamvogue/djgyrofix/actions/workflows/ci.yml/badge.svg)](https://github.com/steamvogue/djgyrofix/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/steamvogue/djgyrofix.svg)](https://pkg.go.dev/github.com/steamvogue/djgyrofix)
[![Go Report Card](https://goreportcard.com/badge/github.com/steamvogue/djgyrofix)](https://goreportcard.com/report/github.com/steamvogue/djgyrofix)
[![License: GPL v3](https://img.shields.io/badge/license-GPL--3.0--or--later-blue.svg)](LICENSE)

A dependency-free Go CLI that detects and corrects high-frequency attitude
artifacts in DJI MP4/MOV metadata, in place, with exact revert.

## Why this exists

If you fly FPV with a DJI air unit, you may have hit this: the raw footage looks
fine, the goggles feed looks fine — and then RockSteady or Gyroflow turns it
into a shaking, twitching mess. Stabilization makes it *worse*.

That complaint has been building across the FPV community. It is a recurring
thread on [r/fpv][reddit] for the O4 Lite, and in May 2026 Oscar Liang traced a
concrete cause on the O4 Pro: DJI [quietly changed the gyro chip][oscar] in
2026-production O4 Pro Air Units, from the MP66 (MPU-6050) to the 1469D
(ICM-40609-D) — the same part already used in the standard O4 and O4 Lite, and
already known for being more sensitive to vibration. DJI's own response to an
affected customer confirmed the change, described it as an improvement in
"precision, noise suppression, and temperature drift control", and said
replacements with the older part were no longer in stock. Mads Tech covers the
same ground [on video][madstech].

Here is the part that matters for this tool. **Gyroflow trusts the recorded
gyro track completely.** It reads those quaternions as ground truth for how far
to counter-rotate each frame. When the track carries a brief artifact — a
telemetry dropout, a corrupted sample, a burst of high-frequency content the
camera never actually experienced — Gyroflow counter-rotates against it with
full confidence, and the correction is more violent than the motion it was
supposed to remove. That is why the stabilized clip can look worse than the
original: not because stabilization failed, but because it succeeded against bad
input.

### What djgyrofix can and cannot do

**It cannot make a noisy IMU into a quiet one.** Nothing in software can. If your
camera is hard-mounted to a resonating frame, the fix is soft mounting, a better
tune, and the other checks in Oscar's article — not this tool.

What it *can* do is repair the recorded track where the artifact is in the data
rather than in the airframe: short dropouts, corrupted samples, and brief
high-frequency bursts that survive into the metadata. It patches those, leaves
genuine motion alone, and hands Gyroflow something it can trust.

It is also a diagnostic. Run `djgyrofix scan` and read the answer:

- **A handful of short events** — that is what this tool is for. Fix them.
- **`--max-affected` refuses because detection flagged a third of the clip** —
  that is the tool telling you the problem is upstream. Smoothing a third of a
  clip would degrade everything and fix nothing. Go and look at the mounting.
- **No events at all, but the footage still shakes after stabilization** — the
  metadata is not what is wrong. Try Gyroflow's Complementary integration method
  or a low-pass filter, both of which pilots report helping on affected units.

Being told *"this is not a metadata problem"* in ten seconds is worth something
on its own, when the alternative is another evening of re-mounting a camera.

## Credit

The hard part of this — finding the quaternions inside an undocumented DJI
protobuf stream and patching them without breaking the container — was worked
out by **Minsu Kim** ([@kim2160][upstream]) in
[DJI Gyro Fix][upstream], a GPL-3.0 Python desktop tool. The field paths, the
variant sniffing, the offset-preserving scanner and the smoothing approach all
come from that work.

djgyrofix is a Go port and rework of it: a CLI instead of a GUI, automatic
detection instead of hand-picked time ranges, and in-place patching with exact
revert instead of writing a full copy. It is held to
[byte-for-byte parity](#golden-parity) with the original, which is the clearest
way to say that the credit belongs upstream.

If you want a GUI, or you are on Windows or macOS and would rather not touch a
terminal, [use Minsu Kim's tool][upstream] — it is signed, notarized, and does
the same core job.

[reddit]: https://www.reddit.com/r/fpv/comments/1mzkd7v/dji_o4_lite_unwatchable_gyroflow_footage_heavy/
[oscar]: https://oscarliang.com/dji-o4-pro-gyro-stabilization-issue/
[madstech]: https://www.youtube.com/watch?v=YibY-87yFko
[upstream]: https://github.com/kim2160/DJIGyroFix

## Try it

The session below runs against a generated fixture (see
[Fixtures](#fixtures)), so you can reproduce it without DJI footage.

```console
$ djgyrofix scan sample.MP4
sample.MP4  wm169  30 s  1500 samples  6000 quaternions @ 200.0 Hz
baseline 0.5 °/s   threshold 60.0 °/s (rolling)

  #  start        end          dur     type     sev  axes   peaks  action
  1  00:00:07.980 00:00:09.220 1.240s  jitter   10.0 X/Z    29     smooth
  2  00:00:14.970 00:00:15.050 0.080s  impact   10.0 Y      1      smooth
  3  00:00:25.000 00:00:25.005 0.005s  dropout  9.0  -      1      bridge

3 events, 1.33 s affected (4.42% of clip)

dry run: would patch 331 quaternions in 85 samples (5296 bytes)
run `djgyrofix fix --apply sample.MP4` to write

$ djgyrofix fix --apply sample.MP4
...
patched 331 quaternions in 85 samples, 5296 bytes
journal: sample.MP4.gyrofix.json
high-frequency residual reduced 99.7%

$ djgyrofix revert sample.MP4
sample.MP4: restored, matches original digest — 5296 bytes (1324 writes)
```

## Working with your own footage

Always scan before you patch. The scan opens the file read-only and tells you
whether this tool is even the right one for the problem.

```bash
djgyrofix scan DJI_0042.MP4
```

If it reports a handful of short events, patch them. `fix` is a dry run until
you pass `--apply`, so you can see exactly what would change first:

```bash
djgyrofix fix DJI_0042.MP4           # what would change
djgyrofix fix --apply DJI_0042.MP4   # do it
```

Then stabilize as usual. Because the metadata is fixed in place, nothing changes
about your Gyroflow workflow:

```bash
gyroflow DJI_0042.MP4 --preset mypreset.gyroflow
```

If the result is not what you hoped, put the file back exactly as it was:

```bash
djgyrofix revert DJI_0042.MP4
```

That is a byte-for-byte restore, not an approximation — the sidecar journal
holds the original value of every four-byte range that was touched. If you would
rather not have originals modified at all, `--out fixed.MP4` writes a patched
copy and leaves the source alone.

For a folder of clips from one session:

```bash
djgyrofix scan --format csv DJI_*.MP4 > events.csv   # review first
djgyrofix fix --apply --jobs 8 DJI_*.MP4
```

**A first run on footage you care about is worth doing on a copy**, whatever the
revert guarantees say. This is beta software operating on your originals.

## What it does

DJI MP4s carry a timed-metadata track (a `djmd` sample entry, or a handler name
containing `dji meta` / `cam meta`) holding per-sample absolute orientation
quaternions in protobuf wire format: four little-endian `float32` values in
`(w, x, y, z)` order.

Gyroflow reads that track as ground truth for how far to counter-rotate each
frame. When the track contains a brief high-frequency artifact — telemetry
dropout, RF corruption, or a genuine sharp impact — Gyroflow's correction over
those samples is abrupt, and stabilized output can look *worse* than the
original footage.

The fix is a byte-level in-place patch: the same four-byte float slots are
overwritten with filtered values. Sample sizes never change, so `stsz`, `stco`
and `co64` stay valid and the container is untouched.

### What it deliberately does not do

**No ffmpeg, no re-encode, no remux.** `-c copy` typically drops or mangles the
private `djmd` track, and any remux rewrites `moov`, invalidating every sample
offset. There is nothing to encode here.

**No protobuf codegen, no math library.** Decoding into structs and
re-serializing loses byte offsets and can change varint lengths. A quaternion
library introduces ordering and rounding divergence from the Python reference,
which would destroy the cheapest validation available — see
[Golden parity](#golden-parity) below.

## Install

### Download a binary

Grab an archive for your platform from the
[releases page](https://github.com/steamvogue/djgyrofix/releases). Builds are
published for Linux, macOS and Windows on both amd64 and arm64, with a
`SHA256SUMS` file alongside them.

```bash
# Linux / macOS
tar xzf djgyrofix-v0.1.0-linux-amd64.tar.gz
sudo install djgyrofix-v0.1.0-linux-amd64/djgyrofix /usr/local/bin/
djgyrofix version
```

On macOS the binary is not notarized, so Gatekeeper will block the first run.
Either right-click → Open, or clear the quarantine flag:

```bash
xattr -d com.apple.quarantine /usr/local/bin/djgyrofix
```

### With Go

Go 1.25 or later. There are no module requirements, so nothing else is
downloaded.

```bash
go install github.com/steamvogue/djgyrofix/cmd/djgyrofix@latest
```

### From source

```bash
git clone https://github.com/steamvogue/djgyrofix.git
cd djgyrofix
make            # fmt, vet, test, build
```

`make dist` cross-compiles release binaries for all six target platforms.

## Commands

| Command  | What it does |
|----------|--------------|
| `scan`   | Analyze and report. Never opens a video for writing. |
| `fix`    | Analyze and patch in place, or to `--out`. Dry run unless `--apply`. |
| `revert` | Restore original bytes from the sidecar journal. |
| `verify` | Check a patched file against its journal. |
| `info`   | Dump track, variant and sample-rate details. |

### Detection flags

```
  --profile string        conservative | balanced | aggressive   (default "balanced")
  --sensitivity float     scales all thresholds, 0.1-3.0         (default 1.0)
  --mad-k float           Hampel sigma multiplier                (default 5.0)
  --baseline-window dur   rolling baseline half-width            (default 5s)
  --floor-dps float       absolute residual floor, °/s           (default 60)
  --min-severity float    ignore events below this score, 0-10   (default 5.0)
  --imu-full-scale float  plausibility gate, °/s                 (default 2000)
```

### Correction flags

```
  --strength float        global multiplier on the weight, 0-1   (default 1.0)
  --smoothing-ms float    override per-event window derivation   (default: auto)
  --bridge-max-samples n  max dropout run to SLERP-bridge        (default 3)
  --no-bridge             disable reconstruction entirely
  --ranges string         manual override: "12.5-14.0,61-62.25"  (skips detection)
```

### Safety and I/O flags

```
  --dry-run               analyze without writing (the default for fix)
  --apply                 actually write
  --backup mode           journal | copy | none                  (default "journal")
  --out FILE              write a patched copy instead of patching in place
  --force                 override idempotency and safety guards
  --max-affected float    refuse if flagged duration exceeds this (default 0.15)
  --variant string        wm169 | wa530 | oq101 | auto           (default "auto")
  --jobs n                files to process in parallel           (default NumCPU)
  --format string         text | json | edl | csv                (default "text")
```

## How detection works

**The signal.** Per-sample angular velocity comes from the delta quaternion,
`Δq = q[i] ⊗ q[i-1]⁻¹`. That velocity is low-passed with a ~12 ms box blur, and
the **residual** — velocity minus its own low-passed copy — is what everything
downstream measures.

The residual is the whole reason automatic detection is viable. A whip-pan or a
flip is *smooth* fast rotation and cancels out entirely; only high-frequency
content survives. A plain velocity threshold would flag every intentional fast
move in the clip.

**The threshold** is a sliding Hampel window rather than one global number:

```
baseline(t)  = median( metric over t ± 5 s )
σ(t)         = 1.4826 · median( |metric − baseline| over t ± 5 s )
threshold(t) = max( floor_dps, baseline + k_mad·σ, k_rel·baseline )
```

The `1.4826` is what makes "5σ" actually mean 5σ; most informal descriptions of
Hampel omit it and silently change sensitivity. Clips under 15 seconds fall back
to the reference's global baseline, because a five-second window cannot slide
over them.

**The plausibility gate** decides what may be *reconstructed* rather than merely
smoothed. Four independent tests condemn a sample: an implied rate above IMU
full scale, a raw quaternion norm far from unity, a timestamp discontinuity or
duplicate decode time, and a single-sample excursion that immediately reverses.

Only samples that fail this gate are eligible for bridging. This is the line
between telemetry corruption, which is safe to reconstruct, and real violent
motion, which is not: bridging a genuine impact fabricates an orientation the
camera never had, and Gyroflow will then mis-correct with full confidence.

**Classification** then splits supra-threshold events four ways:

| Class     | Signature | Action |
|-----------|-----------|--------|
| `dropout` | Short run failing the plausibility gate, valid data either side | SLERP bridge |
| `impact`  | Under 100 ms, single dominant peak, physically plausible | Short-window smooth |
| `jitter`  | Sustained high residual, multiple local peaks | Long-window smooth |
| `motion`  | High residual, but tracking intentional input | **Left alone** |

## How correction works

Dropouts are **interpolated**, not blurred: a SLERP between the last good
orientation before the run and the first good one after, weighted by timestamp.
Blurring would destroy the surrounding motion dynamics, where a SLERP preserves
the real motion exactly and removes only the glitch.

Everything else is smoothed by a **continuous weight envelope**:

```
excess(t) = (metric(t) − threshold(t)) / (k · threshold(t))
w(t)      = boxblur( smoothstep( clamp(excess, 0, 1) ), ~100 ms )
out(t)    = slerp( original(t), smoothed(t), w(t) · strength )
```

Smoothing strength tracks how bad each moment actually is and tapers to zero on
its own. That deletes the reference's entire edge-correction block — the start
and end correction quaternions and their boundary smoothstep — because `w → 0`
continuously at every event edge by construction. There is no discontinuity left
to patch over.

The blur radius is derived per event rather than fixed at one global 180 ms:
roughly 60–100 ms for an impact, and scaled to duration and clamped to 120–400 ms
for jitter.

## Safety

### The patch journal

The patch is size-preserving and small relative to the video, so duplicating a
multi-gigabyte file to enable undo is the wrong trade. `djgyrofix` writes a
sidecar journal instead:

```jsonc
// DJI_0042.MP4.gyrofix.json
{
  "version": 1,
  "tool": "djgyrofix 0.1.0",
  "source":   { "name": "DJI_0042.MP4", "size": 21474836480, "mtime": "..." },
  "track":    { "variant": "wm169", "timescale": 1000, "samples": 36012 },
  "metadata_digest": "sha256:...",   // djmd sample bytes only, pre-patch
  "params":   { "profile": "balanced", "sensitivity": 1 },
  "events":   [ /* the full detection report */ ],
  "writes":   [ { "off": 1043221, "old": "3f7fd0a1", "new": "3f7fce88" } ]
}
```

`revert` restores bit-exact original state from it in milliseconds.
`metadata_digest` covers the `djmd` sample bytes only, so an unrelated container
rewrite does not invalidate a journal while any change to the patched data does.

### Backup modes

| Mode | Behaviour |
|------|-----------|
| `--backup journal` | **Default.** Sidecar journal only. Roughly 0.4% of the video size on heavily-patched footage, far less on a clean clip. |
| `--backup copy` | Full `.orig` copy first. Uses `clonefile` on APFS via `cp -c`, and `copy_file_range` on Linux, which the kernel turns into a reflink on btrfs and XFS. |
| `--backup none` | No undo. Requires `--force`. |
| `--out FILE` | Original untouched; a patched copy is written instead. |

### Write ordering

1. Every patch is computed in memory before the file is opened for writing.
2. The journal goes to a temp file, is fsynced, and is renamed into place —
   **before** the video is touched. If the process dies mid-patch, the journal
   already on disk is what makes the file repairable.
3. Writes are applied and fsynced.
4. The final file size is checked against the journal.
5. A file that already has a journal is refused unless `--force`, which reverts
   first and then re-applies rather than compounding the correction.

`--max-affected` refuses to patch when detection flags more than 15% of a clip.
That is the signature of a bad baseline or genuinely rough footage; blanket
smoothing there degrades stabilization everywhere and fixes nothing.

## Invariants

| # | Invariant |
|---|-----------|
| I1 | Output file size is byte-identical to input size. |
| I2 | Only bytes inside `djmd` sample payloads are ever modified. |
| I3 | Every write is exactly 4 bytes at an offset the protobuf scanner found. No varint is ever re-emitted. |
| I4 | Quaternion component order is `(w, x, y, z)` throughout. |
| I5 | All internal math is `float64`; only the final store is `float32`. |
| I6 | Any patched file reverts to bit-exact original state. |

Each of these has a test that fails if it is broken, in
`cmd/djgyrofix/e2e_test.go`.

## Testing

### Verified on real footage

Everything below is also exercised against a real 6.5 GB DJI clip — 8m17s of
59.94 fps video with 993,523 quaternions at 1978 Hz, three tracks (`hvc1`
video, `djmd` metadata, `dbgi` debug), wm169 layout.

| Step | Result |
|------|--------|
| `info` | Selected the `djmd` track over the decoy `dbgi` track, sniffed wm169, 33.33 quaternions per sample |
| `scan` | 98 events in 9.3 s — 69 jitter, 17 dropout, 12 impact, 5.9% of the clip affected |
| `fix --apply` | 84,133 quaternions in 2,609 samples, 335,761 four-byte writes, 11.4 s |
| `verify` | All 335,761 ranges correct, digest ok, size unchanged, 0.9 s |
| `revert` | 1.3 s, and `sha256sum` matched the original exactly |

The dropouts are the notable part: the plausibility gate found 17 runs of
physically impossible samples in a real recording. That is the failure mode this
tool exists for, and it is not something the synthetic fixtures could have
proven.

Real footage is not committed — it is gigabytes, and the repository has to stay
cloneable. Everything in `make test` runs against generated fixtures.

```bash
make test      # unit, property and end-to-end tests
make race      # the same under the race detector
make cover     # coverage summary
make fuzz      # fuzz both parsers
make parity    # byte-for-byte comparison against the Python reference
```

### Golden parity

The reference implementation, [`kim2160/DJIGyroFix`](https://github.com/kim2160/DJIGyroFix)
v0.92, is fully deterministic. With detection disabled and an explicit
`--ranges`, this port must produce **byte-identical output** to `gyrofix.cli` on
the same input. Anything else is a bug, not a rounding difference.

```bash
git clone --depth 1 https://github.com/kim2160/DJIGyroFix.git ../DJIGyroFix
make parity
```

This is why the numeric core forbids math libraries, and why every `a*b + c` in
`internal/quat` is written with an explicit `float64()` conversion around the
product. Go permits fusing that into a single FMA instruction — and does so on
arm64 — which rounds once instead of twice and diverges from CPython. The
conversion is the spec-sanctioned barrier.

Two things are deliberately *not* held to bit parity, both documented at their
assertions:

- `AngularAccelerationScore` computes `2·acos(cosine)` with `cosine` riding
  against 1.0, where one ULP of difference between Go's and glibc's `acos` moves
  the result by a percent. Nothing is written from that value — it only feeds
  the reported improvement percentage.
- `--strength` below 1.0 has no equivalent flag in the reference CLI, so it is
  covered by the Go-side fixture test against `smooth_quaternions` directly
  rather than end to end.

### Fixtures

Real DJI footage is multi-gigabyte and cannot be committed, so `internal/synth`
builds MP4s that are structurally identical to the parts of a DJI file this tool
touches, with artifacts injected at known times. `tools/mkfixture` exposes the
same generator for manual testing:

```bash
go run ./tools/mkfixture -o sample.MP4 -kind mixed -variant wm169 -seconds 30
./djgyrofix scan sample.MP4
```

`-kind` accepts `clean`, `jitter`, `impact`, `dropout`, `whippan` and `mixed`.
The `whippan` case is the important one: it is fast rotation that detection must
**never** flag, and false positives on intentional motion are the main risk of
automating any of this.

If you have real footage, drop it into the parity harness's `corpus/`
directory and it will be picked up alongside the synthetic files.

## Gyroflow integration

`djgyrofix` fixes the embedded metadata in place, so no handoff format is
needed — chain the two CLIs:

```bash
djgyrofix fix --apply DJI_*.MP4 && gyroflow DJI_*.MP4 --preset mypreset.gyroflow
```

Gyroflow's CLI also supports a watch folder that stabilizes any new video
appearing in a directory, so `djgyrofix` slots in naturally as a pre-processor
writing into it.

For keeping a human in the loop over a batch, `--format edl` or `--format csv`
emits the event ranges for review in an NLE timeline or a spreadsheet, and the
reviewed ranges feed straight back through `--ranges`.

This does not compete with the settings-side workarounds circulating for the
affected O4 units — Gyroflow's Complementary integration method, a low-pass
filter around 5 Hz, better camera soft mounting. Those address a continuously
noisy gyro signal. djgyrofix addresses discrete artifacts in the recorded track.
They are different problems and it is entirely reasonable to need both.

## Known limits

- **It does not fix hardware.** A camera bolted to a resonating frame produces
  a genuinely noisy gyro track, and no amount of post-processing recovers
  orientation the IMU never measured correctly. If `scan` flags a large fraction
  of a clip, that is the answer, not a threshold to raise.
- **Which air units are covered is not fully known.** The three metadata layouts
  this tool understands (`wm169`, `wa530`, `oq101`) come from the upstream
  Python tool and were derived from the cameras its author had. The DJI schema
  is not public, so there is no list mapping models to layouts. Run
  `djgyrofix info --all-variants YOUR_FILE`: if one path finds a plausible
  number of quaternions, you are covered, and if none do, please
  [open an issue](https://github.com/steamvogue/djgyrofix/issues) with that
  output — a new layout is a small change once someone has a file that needs it.
- **Fragmented MP4 is rejected outright.** Samples in `moof` boxes are not
  indexed by this walker, and guessing would put writes at the wrong offsets.
- **Variant sniffing is a heuristic.** It greps the first samples for a model
  string. You will eventually hit a camera it guesses wrong on; `info` prints
  the guess and the field path used, `info --all-variants` shows what each other
  path would find, and `--variant` overrides it.
- **A zero-valued quaternion component has no byte slot.** proto3 omits it, and
  writing a non-zero value there would require resizing the sample. This is a
  hard error, not a silent skip — skipping would leave the orientation partly
  updated and corrupt it.
- **Sub-sample timing is interpolated linearly.** Multiple quaternions share one
  metadata sample's timestamp span, and no per-quaternion timestamp field is
  known in the DJI schema. If one exists, finding it would improve everything
  downstream.
- **Threshold defaults are calibrated against synthetic fixtures**, not a corpus
  of real footage with known-bad sections. Start with `--profile conservative`
  and a dry run on material you care about.

## Licence

GPL-3.0-or-later. This is a port and rework of
[`kim2160/DJIGyroFix`](https://github.com/kim2160/DJIGyroFix) v0.92 (GPL-3.0),
and therefore a derivative work. See [LICENSE](LICENSE).
