# Design notes

This is the design document djgyrofix was built from, kept because the reasoning
behind the unusual choices — no ffmpeg, no protobuf codegen, no math library —
is worth having written down.

**It is the plan, not the record.** A few things looked different once they were
built and measured. The implementation and [real-footage findings](FINDINGS.md)
are the authority where they disagree:

- **§5.2 overstates the failure mode of the reference's global baseline.** The
  plan says one rough segment in a long flight hides everything else. In fact
  the reference takes the median of the *quietest 55%* of bins — the 27.5th
  percentile overall — which resists a single bad segment until roughness covers
  about 72% of the clip. The rolling window is still the right call, but for a
  different and more common reason: a global threshold *over-detects*, flagging a
  rough stretch end to end. Measured on a synthetic clip that is rough for its
  first third, global flags 35% of the footage where rolling flags 2%. See the
  comment on `thresholdCurve` in `internal/detect/detect.go` and the two tests
  named after it.
- **The plan does not mention floating-point contraction.** Go fuses `a*b + c`
  into a single FMA on arm64, which rounds once where CPython rounds twice. That
  alone breaks the byte-parity gate §9.1 depends on. Every product in the numeric
  core carries an explicit `float64()` barrier, and `TestNumericCoreIsNotFused`
  greps the compiled assembly to keep it that way.
- **§10 Tier 3 (`.gcsv` sidecar export) is not implemented.** The plan flags it
  as needing verification against Gyroflow's parser for units, axis convention
  and orientation string. That verification has not happened, and shipping an
  unverified guess would be worse than shipping nothing.
- **§5.1 and §6.2 were replaced after real-footage validation.** The defect is
  frame vibration recorded into the attitude track, surviving as brief
  excursions from the local trend — median 4 ms on the measured clip, spread
  across it rather than confined to manoeuvres. An earlier reading of this as
  an overshoot specific to sharp vector changes came from measurements taken
  before the sample duplication was understood; see docs/FINDINGS.md.
  Detection now compares angular velocity with a ±60 ms local trend, and confirmed events receive an explicit
  correction floor with smooth exterior shoulders. Corrupt samples are bridged
  into the working series before it is filtered.
- **The first implementation is preserved on `study`; the replacement is on
  `main`.** The branch boundary makes the earlier assumptions reproducible
  without leaving production builds on the superseded detector.
- **Milestone estimates were for a human.** Ignore them.

The plan as written follows.

---

# djgyrofix — Go rewrite plan

A dependency-free Go CLI that detects and corrects transient attitude
(quaternion) deviations in DJI MP4/MOV metadata, in place, with exact revert.

Derived from [`kim2160/DJIGyroFix`](https://github.com/kim2160/DJIGyroFix) v0.92
(GPL-3.0). This is a port plus rework, therefore a derivative work: **the Go
implementation must also ship under GPL-3.0.**

---

## 1. What the tool does, precisely

DJI MP4s carry a timed-metadata track (`djmd` sample entry, or a handler name
containing `dji meta` / `cam meta`) holding per-sample **absolute orientation
quaternions** in protobuf wire format, stored as four little-endian `float32`
values in `(w, x, y, z)` order.

Gyroflow reads that track as ground truth for how far to counter-rotate each
frame. When the track contains a brief transient deviation — telemetry dropout,
RF corruption, or vibration recorded into the attitude track —
Gyroflow's correction over those samples is abrupt, and stabilized output can
look *worse* than the original footage.

The fix is a **byte-level in-place patch**: overwrite the same 4-byte float
slots with filtered values. Sample sizes never change, so `stsz`/`stco`/`co64`
stay valid and the container is untouched.

### Non-goals

- **No ffmpeg, no re-encode, no remux.** `-c copy` typically drops or mangles
  the private `djmd` track, and any remux rewrites `moov`, invalidating every
  sample offset. There is nothing to encode here.
- No GUI, no i18n. The original's `ui.py` (822 lines) and `i18n.py` (248 lines)
  are dropped entirely.
- No protobuf schema. The DJI message definitions are not public and are not
  needed.

---

## 2. Core invariants

These are the properties every change must preserve. Violating any one of them
is a P0 bug.

| # | Invariant |
|---|---|
| I1 | Output file size is byte-identical to input size. |
| I2 | Only bytes inside `djmd` sample payloads are ever modified. Never `moov`, never video/audio chunks. |
| I3 | Every write is exactly 4 bytes at an offset discovered by the protobuf scanner. No varint is ever re-emitted. |
| I4 | Quaternion component order is `(w, x, y, z)` throughout — matching DJI and the Python reference, **not** the `(x, y, z, w)` convention used by most math libraries. |
| I5 | All internal math is `float64`; only the final store is `float32`. |
| I6 | Any patched file can be reverted to bit-exact original state. |

**No protobuf codegen.** `google.golang.org/protobuf` decodes into structs and
re-serializes, which loses byte offsets and can change varint lengths. Port the
hand-rolled offset-recording scanner (`protobuf.py`, 155 lines) verbatim.

**No `gonum`, no quaternion library.** The math is a few hundred FLOPs per
sample against a multi-GB file; SIMD buys nothing. A library also introduces
ordering and rounding divergence from the Python reference, which destroys the
cheapest validation available (§9). Hand-roll ~200 lines of quaternion math.

---

## 3. Package layout

```
cmd/djgyrofix/          CLI entrypoint, flag parsing, subcommands
internal/mp4/           ISO-BMFF box walk, sample offset + DTS tables
internal/djiproto/      offset-preserving protobuf scanner, variant paths
internal/quat/          normalize, dot, mul, inverse, slerp, boxblur
internal/detect/        pass 1: velocity, residual, Hampel, classification
internal/correct/       pass 2: bridge, adaptive smooth, weight envelope
internal/patch/         journal, in-place write, reflink copy, revert
internal/report/        JSON / EDL / human-readable output
testdata/golden/        parity fixtures against the Python reference
```

Direct ports (mechanical, low risk):

| Python | LOC | Go package | Effort |
|---|---|---|---|
| `protobuf.py` | 155 | `djiproto` | 0.5 d |
| `mp4.py` | 322 | `mp4` | 1–2 d |
| `smoothing.py` | 195 | `quat` + `correct` | 0.5 d |
| `processor.py` | 375 | `patch` + orchestration | 1 d |
| `detection.py` | 391 | `detect` (reworked, §5) | 2 d |

`mp4.py` handles v0/v1 `mdhd`/`tkhd`, `stco` vs `co64`, and `stsc` chunk→sample
expansion. `github.com/abema/go-mp4` exposes box offsets cleanly if you'd rather
not hand-roll, but the sample-offset reconstruction is the part that matters and
you'll write that yourself either way.

---

## 4. Two-pass architecture

Both passes operate on a single in-memory read of the metadata track. A
20-minute clip is tens of MB of quaternions — streaming with a circular buffer
is a self-imposed constraint, not an optimization, and centered-window filters
need lookahead anyway.

```
  read djmd samples once
        │
        ▼
  ┌───────────────┐
  │  PASS 1       │  velocity → residual → rolling Hampel → classify
  │  detect       │  produces: events[] + per-sample weight envelope w(t)
  └───────┬───────┘
          │  (scan mode stops here and reports)
          ▼
  ┌───────────────┐
  │  PASS 2       │  bridge dropouts / adaptive-smooth jitter
  │  correct      │  produces: patched sample buffers
  └───────┬───────┘
          ▼
  ┌───────────────┐
  │  journal      │  record (offset, original 4 bytes) for every write
  │  + write      │  then seek/overwrite in place
  └───────────────┘
```

---

## 5. Pass 1 — detection

### 5.1 Signal

Ported from `detection.py`, which already gets the core right:

1. Per-sample angular velocity from the delta quaternion:
   `Δq = q[i] ⊗ q[i-1]⁻¹`, angle `2·atan2(|v|, w)`, scaled to °/s by `Δt`.
2. Estimate the local velocity trend (centered box blur, ~60 ms radius).
3. **Residual** = velocity − low-passed velocity.

The residual is the right discriminator and the reason automatic detection is
viable: a smooth whip-pan or flip mostly cancels, while a brief overshoot or
brief excursion from that trend remains visible.

4. Bin residual energy at 10 ms; metric per bin = RMS.

### 5.2 Rolling baseline (the one substantive algorithmic change)

The original computes one global baseline — median of the quietest 55% of bins,
plus MAD — over the analysis window. That works for a hand-picked 3-second range
and **breaks on whole-file scans**: one rough segment in a 12-minute flight
drags the baseline up and hides everything else, and a uniformly jittery clip
detects nothing at all.

Replace with a **sliding Hampel window**:

```
baseline(t) = median( metric over t ± 5 s )
mad(t)      = median( |metric − baseline| over t ± 5 s )
σ(t)        = 1.4826 · mad(t)                 # MAD → σ-equivalent
threshold(t)= max( floor_dps, k_mad·σ(t) + baseline(t), k_rel·baseline(t) )
```

The `1.4826` scale factor is what makes "5σ" mean 5σ; omitting it (as most
informal descriptions of Hampel do) silently changes your sensitivity. Keep the
original's absolute floor (`floor_dps`, default 60 °/s) so still footage with
sensor noise doesn't trip.

### 5.3 Plausibility gate (borrowed idea, tightened)

Before classifying anything as an artifact, test whether the data is
*physically possible*. This is what separates "telemetry corruption" — safe to
reconstruct — from "real violent motion" — must not be touched:

- A nearby pair of opposing over-full-scale transitions returns near the
  pre-entry trajectory and settles (configurable full scale, default 2000 °/s).
- Raw quaternion norm far from unity **before** normalization.
- Sample timestamp discontinuity or duplicate DTS.
- Single-sample discontinuity with immediate return to baseline.

Only samples failing this gate are eligible for reconstruction (§6.1). Everything
else is at most smoothed.

> The suggestion of thresholding on DJI-reported IMU temperature or confidence
> intervals is not actionable: nobody has the schema. The scanner finds
> quaternions by walking blind field paths (`3.3.2.3` for `wm169`, `3.3.4.3` for
> `wa530`, `3.3.2.1.3` for `oq101`) and validating that four `fixed32`s form a
> near-unit norm. There are no known confidence fields.

### 5.4 Event grouping and classification

Group supra-threshold bins with a 200 ms gap tolerance and 20 ms padding (as in
the original), then classify:

| Class | Signature | Action |
|---|---|---|
| `dropout` | 1–3 consecutive quaternions failing the plausibility gate, valid data either side | **Bridge** (§6.1) |
| `impact` | < 100 ms, single dominant peak, passes plausibility | Short-window adaptive smooth |
| `jitter` | ≥ 100 ms sustained high residual, multiple local peaks | Long-window adaptive smooth |
| `motion` | High residual but consistent with intentional input | **Leave alone** |

Note: "1–3 frames" in the informal literature conflates video frames with IMU
samples. There are multiple quaternions per metadata sample, at well above
frame rate — always reason in samples.

---

## 6. Pass 2 — correction

### 6.1 Dropouts → SLERP bridge, not smoothing

For a short run of implausible samples, blurring destroys the surrounding motion
dynamics. Instead, slerp between the last good quaternion before and the first
good one after, weighted by timestamp. This preserves real motion exactly and
removes only the glitch.

**Gate this on §5.3.** Bridging a genuine impact fabricates orientation the
camera never had, and Gyroflow will then mis-correct with full confidence.
Default `--bridge-max-samples 3`; refuse to bridge longer runs.

### 6.2 Jitter → event-confidence envelope

This replaces the original's binary time windows and is the bigger win: it makes
manual range entry obsolete rather than merely automating it.

```
excess(t)  = (metric(t) − threshold(t)) / (k · threshold(t))
w_event    = max(event_confidence, max smoothstep(clamp(excess(t), 0, 1)) in event)
w(t)       = w_event across the event, with ~100 ms smooth shoulders
out(t)     = slerp(working(t), smoothed(t), w(t) · strength)
```

Detection confidence supplies a meaningful correction floor throughout a
confirmed event; peak excess can raise the whole event to full correction. One
stable core weight avoids manufacturing blend transitions at the 10 ms bin
boundaries. The exterior shoulders taper to zero without attenuating the event
core. Confirmed corrupt samples are bridged before the same working series is
filtered, so contained dropout and jitter corrections share their boundaries.

Rescan for at most three correction passes. A newly exposed smoothing event may
join a later pass only while the union of authorized ranges remains below
`--max-affected`. Never add a newly detected dropout during re-evaluation.

Retain from the original: sign unwrapping before filtering (`if dot(prev,q) < 0:
negate`), three box-blur passes as a Gaussian approximation, renormalization
after every operation, and restoring each output quaternion's original sign
before writing.

### 6.3 Per-event smoothing window

One global 180 ms is wrong for both classes. Derive the blur radius per event:

- `impact` → short window, ~60–100 ms
- `jitter` → scale to event duration, clamped to ~120–400 ms

Or derive it from the dominant residual frequency within the event. Start with
duration-based; it's simpler and testable.

---

## 7. In-place patching with exact revert

### 7.1 The patch journal (headline feature)

The patch is size-preserving and tiny — a few thousand 4-byte writes at most.
So instead of duplicating a 20 GB file to enable undo, write a sidecar journal:

```jsonc
// DJI_0042.MP4.gyrofix.json
{
  "version": 1,
  "tool": "djgyrofix 0.2.0",
  "created": "2026-08-31T10:14:22Z",
  "source": { "name": "DJI_0042.MP4", "size": 21474836480, "mtime": "..." },
  "track": { "variant": "wm169", "timescale": 1000, "samples": 36012 },
  "metadata_digest": "sha256:...",   // djmd sample bytes only, pre-patch
  "params": { "profile": "balanced", "sensitivity": 1.0, "...": "..." },
  "events": [ /* full detection report */ ],
  "writes": [ { "off": 1043221, "old": "3f7fd0a1", "new": "3f7fce88" } ]
}
```

`djgyrofix revert file.MP4` restores bit-exact original state from the journal
in milliseconds. `metadata_digest` catches the case where the file changed
underneath you.

This is strictly better than a full backup copy for the common case, and costs
kilobytes.

> **Measured:** on a real 6.5 GB clip with 98 detected events this comes to
> 335,761 writes and a 28 MB journal — 0.4% of the file, not kilobytes. The
> trade still holds overwhelmingly (11 s to patch, 1.3 s to revert, against
> copying 6.5 GB), but the plan understated the size for a long, busy clip.

### 7.2 Backup strategy

| Mode | Behaviour |
|---|---|
| `--backup journal` | **Default.** Sidecar journal only. Instant, ~KB. |
| `--backup copy` | Full `.orig` copy first. Uses `copy_file_range` (Linux), `clonefile` (APFS — instant, zero space), `CopyFileEx` (Windows). |
| `--backup none` | No undo. Requires `--force`. |
| `--out FILE` | Original untouched; write a patched copy (the v0.92 behaviour). |

On APFS — which is most of the user base — `clonefile` makes `--backup copy`
effectively free, so offer it prominently on macOS.

### 7.3 Write safety

1. Compute all patches in memory before opening the file for writing.
2. Write journal to a temp file, `fsync`, `rename` — **before** touching the video.
3. Apply writes, `fsync`, close.
4. Verify final file size against `source.size` (I1).
5. Refuse to patch a file that already has a journal unless `--force`
   (which reverts first, then re-applies) — idempotency guard.

---

## 8. CLI surface

```
djgyrofix <command> [flags] <file...>

Commands:
  scan     analyze and report; never writes to the video
  fix      analyze and patch in place (or to --out)
  revert   restore original bytes from the sidecar journal
  verify   check a patched file against its journal
  info     dump track/variant/sample-rate details
```

### Detection flags

```
  --profile string        conservative | balanced | aggressive   (default "balanced")
  --sensitivity float     scales all thresholds, 0.1–3.0         (default 1.0)
  --mad-k float           Hampel σ multiplier                    (default 5.0)
  --baseline-window dur   rolling baseline half-width            (default 5s)
  --floor-dps float       absolute residual floor, °/s           (default 60)
  --min-severity float    ignore events below this score, 0–10   (default 5.0)
  --imu-full-scale float  plausibility gate, °/s                 (default 2000)
```

### Correction flags

```
  --strength float        global multiplier on w(t), 0–1         (default 1.0)
  --smoothing-ms float    override per-event window derivation   (default: auto)
  --bridge-max-samples n  max dropout run to SLERP-bridge        (default 3)
  --no-bridge             disable reconstruction entirely
  --ranges string         manual override: "12.5-14.0,61-62.25"  (skips detection)
```

`--ranges` keeps the v0.92 escape hatch and makes `scan` output round-trippable:
review the report, hand-edit, feed it back.

### Safety and I/O flags

```
  --dry-run               default for `fix` without --apply
  --apply                 actually write
  --backup mode           journal | copy | none                  (default "journal")
  --out FILE              write a copy instead of patching in place
  --force                 override idempotency and safety guards
  --max-affected float    refuse if flagged duration exceeds this fraction (default 0.15)
  --variant string        wm169 | wa530 | oq101 | auto           (default "auto")
  --jobs n                parallel files                          (default NumCPU)
  --format string         text | json | edl | csv                 (default "text")
```

**`--max-affected` matters.** If detection flags more than ~15% of a clip,
that's the signature of a bad baseline or genuinely rough footage — blanket
smoothing there degrades stabilization everywhere and fixes nothing. Fail loudly
rather than silently mangling the file.

**`--variant`** is needed because auto-detection is a heuristic: the original
greps the first 5 samples' first 1 KB for `"oq101"` / `"wa530"` and otherwise
assumes `wm169`. You will hit a camera it guesses wrong on.

### Example session

```console
$ djgyrofix scan DJI_0042.MP4
DJI_0042.MP4  wm169  1042 s  36012 samples @ 199.8 Hz
baseline 18.4 °/s   threshold 71.2 °/s (rolling)

  #  start        end          dur     type     sev  axes   peaks
  1  00:01:12.480 00:01:12.560 0.080s  dropout  9.1  Y      1
  2  00:03:44.120 00:03:44.910 0.790s  jitter   6.8  X/Z    9
  3  00:08:01.220 00:08:01.300 0.080s  impact   7.4  X      1

3 events, 0.95 s affected (0.09% of clip)

$ djgyrofix fix --apply DJI_0042.MP4
patched 3 events, 1,284 quaternions, 5,136 bytes
journal: DJI_0042.MP4.gyrofix.json
transient residual reduced 71.3%

$ djgyrofix revert DJI_0042.MP4
restored 5,136 bytes — file matches original digest
```

---

## 9. Validation

### 9.1 Golden parity against Python (the primary gate)

The original is fully deterministic. With `--ranges` and detection disabled, the
Go implementation must produce **byte-identical** output to `gyrofix.cli` on the
same inputs. Anything else is a bug, not a rounding difference.

```
for f in testdata/*.MP4; do
  python -m gyrofix.cli "$f" 12.5 14.0 --output py.MP4
  djgyrofix fix --ranges 12.5-14.0 --out go.MP4 "$f"
  cmp py.MP4 go.MP4 || exit 1
done
```

This is why §2 forbids math libraries. Build this harness in M1, before writing
any of the new detection logic — it validates the port and then guards every
subsequent change.

### 9.2 Unit tests

`tests/` in the original builds synthetic protobuf samples with tiny varint and
`fixed32` helpers. Those translate directly to Go table tests and cover the
scanner, time parsing, and interval merging.

### 9.3 Property and fuzz tests

- **Round-trip**: `fix` then `revert` yields a bit-identical file, for every fixture.
- **Invariant I1/I2**: after any `fix`, all bytes outside recorded journal offsets are unchanged.
- **Fuzz the MP4 and protobuf parsers** with `go test -fuzz`. Both walk
  attacker-controllable length fields; the Python version already raises on
  out-of-bounds, and Go must too rather than panicking or allocating wildly.
- **No-op safety**: a clean clip with no events must produce zero writes and no journal.

### 9.4 Synthetic detection fixtures

Generate metadata tracks with injected artifacts of known type, duration, and
amplitude; assert the classifier recovers them and, critically, that a synthetic
**whip-pan produces zero events**. False positives on intentional motion are the
main risk of automation.

### 9.5 Pinned detection tables

§9.1 is the release gate and it does not cover any of §9.5's ground. Parity runs
`--ranges`, which skips detection entirely: it proves the numeric core matches
the Python reference byte for byte and says nothing about which events are found
or how they are classified. Detection can be badly wrong with parity green.

So `TestDetectionGolden` pins what pass 1 decides and what pass 2 plans in
response — the event table with each event's class, action, severity, peak rate,
baseline ratio, axes, peak count and derived window, plus the noise profile,
run-repair counts, write counts, residual scores and verdict. Tables live in
`testdata/golden/detection/`. A change to any of it fails the test and prints
the first line that moved; regenerate with:

```
go test ./cmd/djgyrofix -run TestDetectionGolden -update
```

Then read the diff. **The diff is the review** — the harness cannot tell a fix
from a regression, only that behaviour moved.

What a diff means depends on the fixture, and the two kinds are not equivalent:

- **Synthetic** cases carry ground truth by construction, since the artifact was
  injected at a known time with a known shape. They run everywhere, including
  CI. `synth-whippan` finding anything actionable is a false positive on
  intentional motion, per §9.4.
- **Corpus** cases carry none. Nobody has labelled which 140 ms of real footage
  is a wobble and which is a control input, so those tables pin behaviour, not
  correctness. Treat a diff there as a question to answer at the timestamps that
  moved — never as a verdict.

Two limits worth stating plainly. The corpus clips are multi-gigabyte and
gitignored, so those cases skip anywhere the file is absent, CI included; their
tables are committed regardless, because a table only one machine can regenerate
is still worth reading on any of them, but it also means a contributor without
the footage will not be told when their change moves it. And the residual score
cannot referee a detection change: it is measured with the same detector whose
definition of residual just moved, so the target and the ruler shift together.
That is why the tables record the event set itself and not only the score.

### 9.6 Labelled corpus scoring

§9.5 pins what detection decides; it cannot say whether deciding it was right.
§13.4 wants that answered by scoring against clips a human has judged, weighting
false positives above misses.

`TestCorpusLabelFiles` is the scoring half, built ahead of the footage. Reviewer
labels live in `testdata/corpus/<clip>.labels.csv` as `start,end,verdict` rows
with verdict `artifact`, `motion` or `unsure`; the intake procedure and the
format are in `testdata/corpus/README.md`. Label files are syntax-checked
wherever the tests run, CI included, and scored only where the clip is present.

Two decisions are worth stating. Precision and recall are computed over labelled
ground only — an actionable event overlapping no label is reported separately
rather than counted against precision, because a reviewer labels what they
review and treating the remainder as either correct or incorrect would be an
invention. And `unsure` is counted but never scored, since folding an undecided
judgement into a ratio makes the number look more certain than the review behind
it.

`TestSyntheticCorpusScores` runs the same scorer against labels derived from the
constants `synth` injects its artifacts at, which is ground truth by
construction. That exercises the scoring path today and states §9.4's whip-pan
invariant in the terms §13.4 will use: no profile may act on it.

---

## 10. Gyroflow integration

Tiered by confidence. Ship tier 1 first; treat the rest as opt-in.

### Tier 1 - chain the CLIs (works today, zero coupling)

Gyroflow ships a CLI accepting input files plus flags for parallel renders,
output and sync parameters, project export, presets, a watch folder and an
external gyro file. Since `djgyrofix` fixes the embedded metadata in place, no
handoff format is needed:

```bash
djgyrofix fix --apply DJI_*.MP4 && gyroflow DJI_*.MP4 --preset mypreset.gyroflow
```

Its watch folder automatically stabilizes any new video appearing in a specified
directory, so `djgyrofix` slots in naturally as a pre-processor writing into
that folder.

### Tier 2 - machine-readable reports

`--format json` for scripting; `--format edl` or `csv` emitting event ranges for
review in an NLE timeline before applying. This is the cheapest way to keep a
human in the loop over a batch.

### Tier 3 - sidecar gyro log (non-destructive alternative)

The Gyroflow CLI accepts an external gyro file via `-g`. Differentiating the
corrected quaternions back into angular velocity and emitting a `.gcsv` would
let you stabilize with corrected data **without ever writing to the MP4** - the
safest possible mode.

Flagged as needs-verification: `.gcsv` carries raw gyro/accel samples, so units,
axis convention and orientation string all need to be confirmed against
Gyroflow's parser before this is trustworthy. Prototype and diff against
Gyroflow's own `--export-metadata` output on a known-good file.

**Status: not implemented.** That verification never happened, and shipping an
unverified guess would be worse than shipping nothing.

### Not recommended

Generating `.gyroflow` project JSON directly. The format is internal and
versioned; you'd be chasing upstream changes for no benefit over Tier 1.


## 11. Milestones

| # | Deliverable | Days |
|---|---|---|
| **M0** | Repo scaffold, GPL-3.0, CI matrix (linux/darwin/windows × amd64/arm64) | 0.5 |
| **M1** | `mp4` + `djiproto` ports; `info` subcommand; **golden parity harness (§9.1)** | 2–3 |
| **M2** | `quat` + `correct` ports; `fix --ranges --out`; byte-identical to Python | 1.5 |
| **M3** | In-place patching, journal, `revert`, `verify`, reflink backup | 1.5 |
| **M4** | `detect` port + rolling Hampel + plausibility gate; `scan` subcommand | 2 |
| **M5** | Weight envelope, SLERP bridging, per-event windows, `--profile` presets | 2 |
| **M6** | Batch mode, `--jobs`, JSON/EDL output, Gyroflow tier 1–2 | 1 |
| **M7** | Fuzzing, synthetic fixtures, release binaries | 1 |

**~11–13 days total.** M1–M3 alone (~5 days) already yield a shippable
dependency-free CLI at feature parity with v0.92, plus in-place patching and
revert — which the original doesn't have.

---

## 12. Risks

| Risk | Mitigation |
|---|---|
| Auto-detection smooths intentional motion | Residual signal already rejects smooth fast rotation; `motion` class; `--max-affected` guard; dry-run default; synthetic whip-pan test |
| Bridging a real impact fabricates orientation | Plausibility gate (§5.3) is a hard precondition; `--bridge-max-samples 3` |
| Variant sniffing guesses wrong | `--variant` override; `info` prints the guess and the field path used |
| proto3 omits a zero-valued component, so no byte slot exists to write | Reproduce the original's hard error — never silently skip, that corrupts orientation |
| Fragmented MP4 (`moof`) | Explicit rejection, as in the original — do not attempt |
| Off-by-one vs. Python in sample range selection | The original deliberately over-reads one sample each side and linearly interpolates intra-sample times (`subindex / len(refs)`). Port literally; the parity harness catches deviation |
| Interrupted write leaves a half-patched file | Journal written and fsynced first; `verify` detects; `revert` repairs |
| Rolling baseline degenerates on very short clips | Fall back to the original global baseline below ~15 s |

---

## 13. Open questions

1. **Quaternion rate per camera.** Sample rate drives the box-blur radii and
   window defaults. Measure across `wm169` / `wa530` / `oq101` before fixing
   defaults; `info` should print it.
2. ~~**Do multiple quaternions per metadata sample carry real sub-sample
   timing**, or is the original's linear interpolation the best available?~~
   **Answered: they do not.** Every one of the 1,449,040 quaternion messages
   across both clips holds four `fixed32` fields and nothing else. Timing is
   per-sample only, from a microsecond timestamp in the container message, so
   linear interpolation across the sample is the best available rather than a
   compromise. The same container carries a sample counter that would identify a
   dropped metadata sample directly; `info` prints both. See FINDINGS.
3. **Is `.gcsv` viable as a lossless handoff** (Tier 3), or does the round-trip
   through angular velocity lose too much?
4. **Threshold calibration** needs a corpus. Collect clips with
   known-bad sections plus clips with aggressive-but-clean flying, and tune
   `--profile` presets against both — false positives matter more than misses.
