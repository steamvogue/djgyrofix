# Labelled corpus

This directory holds reviewer labels for real clips. The clips themselves are
never committed — they are multi-gigabyte and gitignored — but the labels are
small, and they are the only thing here that cannot be regenerated.

## Why this exists

Detection has no ground truth on real footage. The pinned tables in
`testdata/golden/detection/` record what the detector *decides*; nothing in them
can say whether deciding it was right. Every detector change so far has been
justified by physical reasoning plus synthetic fixtures, then accepted on event
count deltas — and one settings change moved a real clip from 108 events to 11.
That is a large swing with no way to say which end was closer to correct.

A label file fixes that for one clip. With it, `TestCorpusLabelFiles` reports
precision and recall per profile, and DESIGN §13.4 becomes a number rather than
a judgement call.

**False positives matter more than misses.** Correcting a region that was
actually intentional flight invents an orientation the aircraft never held. A
miss only leaves the footage as it already was. Weight the review accordingly:
when unsure whether something was a defect or a control input, label it `unsure`
rather than guessing.

## Contributing a clip

1. **Drop the clip in `testdata/`**, keeping its original name. It stays
   gitignored; only you and anyone you send it to will have it.

2. **Get a starting list of times.** The CSV export carries every column the
   review needs:

   ```bash
   djgyrofix scan --format csv DJI_0042.MP4 > review.csv
   ```

3. **Watch the footage stabilized in Gyroflow** and judge each listed time.
   Then — this part matters — **scrub the rest of the clip too**. A sheet seeded
   from detection can only ever confirm what the detector already found; the
   misses are the half that has to come from your eyes.

4. **Write `testdata/corpus/DJI_0042.labels.csv`.** The name before
   `.labels.csv` must match the clip's name without its extension, which is how
   the two are paired.

5. **Run the scorer:**

   ```bash
   go test ./cmd/djgyrofix -run TestCorpusLabelFiles -v
   ```

   Label files are syntax-checked everywhere, including in CI with no footage
   present, so a malformed file fails before anyone tries to use it. Scoring
   only runs where the clip is.

## Label format

```csv
# clip: DJI_0042.MP4, O4 Pro, soft-mounted, reviewed in Gyroflow 1.6.3
# reviewer: someone, 2026-09-03
start,end,verdict,note
8.00,9.20,artifact,vibration burst, whole window shakes
15.00,15.06,artifact,single hit on landing gear
20.00,20.90,motion,deliberate whip pan
41.20,41.40,artifact,detector missed this one entirely
52.10,52.30,unsure,brief, could be either
```

Seconds from the start of the clip. Lines beginning `#` are comments — use them
to record the camera, the mount, and who reviewed it, since that context decides
what the numbers are worth later. A `start,end,...` header line is optional.

| verdict | meaning | scored as |
|---|---|---|
| `artifact` | a genuine defect | hit if detection acts on it, otherwise a miss |
| `motion` | intentional flight | **false positive** if detection acts on it |
| `unsure` | reviewed, undecided | counted, never scored |

Ranges need not line up with detector boundaries; matching allows 0.1 s either
side, which is roughly the precision of a time read off a scrubber.

## How the score is read

Precision and recall are computed **over labelled ground only**. An actionable
event overlapping no label is reported as `unlab.` rather than counted against
precision — you labelled what you reviewed, and treating the rest as either
correct or incorrect would be an invention. A large `unlab.` count means the
review was partial, not that detection was wrong.
