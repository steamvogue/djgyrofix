# Command reference

Every flag djgyrofix accepts, what it does, and the recipes worth keeping.

- [The five commands](#the-five-commands)
- [Correction modes](#correction-modes)
- [Recipes](#recipes)
- [Undo](#undo)
- [Detection flags](#detection-flags)
- [Correction flags](#correction-flags)
- [Safety flags](#safety-flags)
- [Output flags](#output-flags)
- [If it still shakes](#if-it-still-shakes)
- [Comparing settings by eye](#comparing-settings-by-eye)
- [Which knobs actually matter](#which-knobs-actually-matter)
- [Which version am I running](#which-version-am-i-running)
- [Exit codes](#exit-codes)

## The five commands

| Command | What it does |
|---|---|
| `scan` | Analyze and report. Opens the video read-only; never writes. |
| `fix` | Analyze and patch. **Dry run unless you pass `--apply`.** |
| `revert` | Restore the original bytes from the sidecar journal. |
| `verify` | Check a patched file against its journal. |
| `info` | Dump track, variant and sample-rate details. |

Always scan first. It costs seconds even on a 6 GB file and tells you whether
this is the right tool for the problem.

```bash
djgyrofix scan DJI_0042.MP4
```

## Correction modes

Two ways to correct a detected event, chosen with `--repair`.

```bash
# runs (default) — replace only the samples that are out of trend
djgyrofix fix --apply DJI_0042.MP4

# blur — low-pass the whole event, the behaviour before 0.8.0
djgyrofix fix --apply --repair blur DJI_0042.MP4
```

`blur` smooths everything inside a detected event. It was the default through
0.7.x, and it is the path held to byte-for-byte parity with the Python
reference.

`runs` finds the supra-threshold runs *inside* an event and interpolates each
along the arc between its neighbours, leaving every other sample untouched. It
exists because the artifact is short: on real footage the median run is about
4 ms inside events spanning hundreds of milliseconds, so the blur was smoothing
genuine motion to remove a brief overshoot.

A run is selected by the part of the residual **perpendicular to the rotation
axis**, not by its plain magnitude. Turning faster or slower than the local
trend about the axis you are already turning about is flying; the axis itself
moving is the artifact. On the real clip a 366 °/s exit rotation reads 29%
across-axis where the wobble 400 ms later reads 92%, so this is what stops a
repair cutting into an aggressive manoeuvre.

It can fail in a way the blur cannot: these runs cluster on
sharp movements, where an interpolation is most likely to invent an orientation
the aircraft never held. Two guards bound that. A run longer than 30 ms is never
replaced, and a run is only replaced when its endpoints still match the
surrounding motion — a deviation that departs and *stays* departed is read as
real motion and left alone. The report says what happened:

```
run-repair: replaced 4901 runs (29112 quaternions); 400 too long to
interpolate, 12 were real motion
```

There is also a manual path that skips detection entirely:

```bash
djgyrofix fix --apply --ranges 12.5-14.0,61-62.25 DJI_0042.MP4
```

Times accept seconds (`22.5`) or clock form (`00:00:22.500`, `1:02.5`).

`--ranges` always corrects by blur, whatever `--repair` says, and warns if you
asked for the other mode. Run-repair picks its runs out of a detected event,
and this path runs no detection: the window you named is the whole instruction.
It is also the path held to byte-for-byte parity with the Python reference,
which has no notion of run-repair.

## Recipes

**Look before you leap.** `fix` without `--apply` reports exactly what it would
change and writes nothing:

```bash
djgyrofix fix DJI_0042.MP4
djgyrofix fix --repair blur DJI_0042.MP4
```

**Never touch the original.** Writes a patched copy and leaves the source alone:

```bash
djgyrofix fix --apply --out fixed.MP4 DJI_0042.MP4
```

**Detect more.** The absolute floor is the strongest knob; 60 is the default and
20 is a reasonable step down on footage that still shakes:

```bash
djgyrofix fix --apply --floor-dps 20 DJI_0042.MP4
```

**Let it choose.** Autopilot picks the profile and refuses footage no profile
can help:

```bash
djgyrofix scan --auto DJI_0042.MP4          # what it would choose, and why
djgyrofix fix --auto --apply DJI_0042.MP4   # choose and patch
```

**Review a folder before patching it:**

```bash
djgyrofix scan --format csv DJI_*.MP4 > events.csv
djgyrofix fix --apply --jobs 8 DJI_*.MP4
```

**Review the flagged ranges in an NLE:**

```bash
djgyrofix scan --format edl DJI_0042.MP4 > events.edl
```

**Machine-readable, for scripting:**

```bash
djgyrofix scan --format json DJI_0042.MP4 | jq '.advice.verdict'
djgyrofix scan --format json DJI_0042.MP4 | jq '.noise'
```

**Compare two settings side by side:**

```bash
djgyrofix fix --apply --floor-dps 20 --out runs.MP4 DJI_0042.MP4
djgyrofix fix --apply --floor-dps 20 --repair blur --out blur.MP4 DJI_0042.MP4
```

## Undo

Every applied patch writes a sidecar journal next to the video holding the
original value of every byte it touched.

```bash
djgyrofix verify DJI_0042.MP4               # does the patch match its journal?
djgyrofix revert DJI_0042.MP4               # bit-exact restore
djgyrofix revert --keep-journal DJI_0042.MP4
djgyrofix revert --force DJI_0042.MP4       # repair a half-written patch
```

Re-patching a file that already carries a journal is refused. `--force` reverts
first and then re-applies, so corrections never compound:

```bash
djgyrofix fix --apply --force --floor-dps 20 DJI_0042.MP4
```

## Detection flags

Accepted by `scan` and `fix`.

| Flag | Default | What it does |
|---|---|---|
| `--profile` | `balanced` | `conservative`, `balanced`, `aggressive`. Moves the floor, the Hampel multiplier and the severity cut together. |
| `--style` | `normal` | `cinematic` (±5 s), `normal` (±12 s), `freestyle` (±20 s). Sets the rolling baseline window only. |
| `--sensitivity` | `1.0` | Scales every threshold, 0.1–3.0. Higher detects more. |
| `--mad-k` | from profile | Hampel sigma multiplier. |
| `--baseline-window` | from `--style` | Rolling baseline half-width, as a duration. Overrides `--style`. |
| `--floor-dps` | from profile | Absolute residual floor in °/s. 90 / 60 / 40 by profile. |
| `--min-severity` | from profile | Ignore events scoring below this, 0–10. |
| `--imu-full-scale` | `2000` | Plausibility gate in °/s. |
| `--auto` | off | Pick the profile automatically; refuse footage no profile can fix. |

## Correction flags

| Flag | Default | What it does |
|---|---|---|
| `--repair` | `runs` | `runs` or `blur`. See [Correction modes](#correction-modes). |
| `--strength` | `1.0` | Global multiplier on the correction weight, 0–1. Zero is a no-op. |
| `--smoothing-ms` | auto | Override the per-event blur window. |
| `--bridge-max-samples` | `3` | Longest dropout run that may be SLERP-bridged. |
| `--no-bridge` | off | Never reconstruct a dropout. |
| `--ranges` | — | Manual time ranges; skips detection entirely, and always blurs. |

## Safety flags

`fix` only.

| Flag | Default | What it does |
|---|---|---|
| `--apply` | off | Actually write. Without it, `fix` is a dry run. |
| `--dry-run` | on | Analyze and report without writing. |
| `--out FILE` | — | Write a patched copy; leave the original untouched. |
| `--backup` | `journal` | `journal` (sidecar), `copy` (full `.orig`), `none` (needs `--force`). |
| `--max-affected` | `0.15` | Refuse if detection flags more than this fraction of the clip. |
| `--force` | off | Override the idempotency and safety guards. |

## Output flags

| Flag | Default | What it does |
|---|---|---|
| `--format` | `text` | `text`, `json`, `edl`, `csv`. |
| `--variant` | `auto` | `wm169`, `wa530`, `oq101` or `auto`. Overrides sniffing. |
| `--jobs` | CPU count | Files processed in parallel. |

`info` also takes `--all-variants`, which shows what every field path would find
— useful when sniffing guesses wrong on a camera nobody has tested.

## If it still shakes

Work down this ladder. Each step is a real thing to try, in the order that pays
off, and the last two are the honest answer when the metadata is not the
problem.

**1. Switch correction mode.** Run-repair is the default and replaces only the
samples that are out of trend; the blur smooths a whole event. If a repair looks
worse, try the other one — they fail differently.

```bash
djgyrofix fix --apply --force --repair blur DJI_0042.MP4
```

**2. Lower the floor.** This is the strongest knob in the tool. The default of
60 °/s only admits transients far above the real noise floor, which on measured
footage sits near 3 °/s.

```bash
djgyrofix fix --apply --force --floor-dps 20 DJI_0042.MP4
```

Below about 10 the false positives start: on a clip known to be good, 15 still
reports one event and 10 reports six. Twenty is the recommended step.

**3. Check the verdict, not just the events.** If `scan` says `upstream`, no
amount of tuning will help — the noise floor itself is the defect.

```bash
djgyrofix scan --format json DJI_0042.MP4 | jq '.advice'
```

**4. Accept the ceiling.** Three things are mixed together in shaky stabilized
footage and this tool addresses only the first:

- Transients in the metadata — what it repairs.
- Genuine airframe motion that stabilization amplifies. Soft mounting, props,
  tune.
- Rolling-shutter wobble from the sensor, which is not in the metadata at all.
  Gyroflow's own rolling-shutter correction handles it, given the right readout
  time.

**5. Try it on Gyroflow's side.** Pilots report the Complementary integration
method and the low-pass filter helping on affected units.

## Comparing settings by eye

No metric in this tool has proved as reliable as watching the stabilized output,
so the workflow that matters is producing candidates and comparing them. `--out`
never touches the original, so you can make as many as you like:

```bash
djgyrofix fix --apply --floor-dps 20 --out A_runs.MP4 DJI_0042.MP4
djgyrofix fix --apply --floor-dps 20 --repair blur --out B_blur.MP4 DJI_0042.MP4
djgyrofix fix --apply --profile aggressive --floor-dps 20 --out C_aggr.MP4 DJI_0042.MP4
```

Then stabilize all three and watch the same manoeuvre in each. Read the
predicted numbers as a hint, not a verdict:

```bash
djgyrofix fix --floor-dps 20 --format json DJI_0042.MP4 \
  | jq '{clip: (1 - .clip_score_after/.clip_score_before), region: (1 - .score_after/.score_before), repair}'
```

The clip-wide figure falls when correction misses something. The in-region
figure cannot, because it only measures where detection looked — read them as a
pair, and remember a low clip-wide number on otherwise clean footage just means
there was little wrong to begin with.

## Which knobs actually matter

Not all of these bite on all footage, and it is better to know which.

The threshold is `max(--floor-dps, baseline + k·σ, k_rel·baseline)`. On real DJI
footage the measured noise floor is around 2–3.4 °/s, so the two adaptive terms
land far below the absolute floor and never win. In practice that means:

**Live on any footage**

- `--repair`, `--strength`, `--smoothing-ms`, `--no-bridge`, `--ranges`
- `--floor-dps` — the strongest knob by a distance
- `--min-severity`, `--max-affected`
- `--profile`, but only through the floor it sets

**Currently inert on clean-floor footage**

- `--style`, `--baseline-window`, `--mad-k`
- the non-floor parts of `--profile` (`rel-k`, `motion-ratio`)

They are not decorative in principle — they drive the threshold whenever the
adaptive terms exceed the floor, which happens on genuinely noisy footage and on
any clip once the floor is lowered past roughly 14 °/s. They simply have nothing
to do while the floor sits above them.

## Which version am I running

Flags are added over time, and an older binary answers an unknown flag by
printing its usage block — which looks like a syntax error and is not one.

```bash
djgyrofix version
djgyrofix fix --help          # every flag this build accepts
```

If a flag from this document is missing, the binary predates it. Tagged builds
are on the [releases page](https://github.com/steamvogue/djgyrofix/releases/latest);
every push to `main` also publishes Windows builds as 30-day artifacts, which is
where a feature lands before it is released.

Feature availability:

| Flag | Since |
|---|---|
| `--auto` | 0.3.0 |
| `--style` | 0.4.0 |
| `--repair runs` | 0.6.0 (opt-in), 0.8.0 (default) |
| across-axis run selection | 0.7.0 |

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | One or more files failed; the reason is on stderr. |
| 2 | Bad usage — an unknown flag or an invalid value. |

A refusal — over `--max-affected`, an existing journal, an autopilot stop — is a
failure, not a silent skip. Scripts can rely on that.
