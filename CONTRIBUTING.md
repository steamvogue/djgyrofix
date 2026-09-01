# Contributing to djgyrofix

Thanks for looking. This is a small tool with one unusual constraint, and
knowing it up front will save you time.

## The constraint

djgyrofix is a port of [`kim2160/DJIGyroFix`](https://github.com/kim2160/DJIGyroFix)
v0.92, and with detection disabled it must produce **byte-identical output** to
the Python original on the same input. That is not an aspiration — it is a test
that runs in CI on every push, and it is the main reason this port can be
trusted against footage nobody here has.

```bash
git clone --depth 1 https://github.com/kim2160/DJIGyroFix.git ../DJIGyroFix
make parity
```

If your change moves those bytes, that is not automatically wrong — but the pull
request has to say so and explain why.

## Getting set up

Go 1.25 or later. There are no module requirements, so nothing is downloaded to
build or test.

```bash
git clone https://github.com/steamvogue/djgyrofix.git
cd djgyrofix
make            # fmt, vet, test, build
```

You do not need DJI footage. `internal/synth` builds MP4s that are structurally
identical to the parts of a real file this tool touches, with artifacts injected
at known times:

```bash
go run ./tools/mkfixture -o sample.MP4 -kind mixed -seconds 30
./djgyrofix scan sample.MP4
```

`-kind` accepts `clean`, `jitter`, `impact`, `dropout`, `whippan` and `mixed`.

## Make targets

| Target | What it does |
|--------|--------------|
| `make` | fmt, vet, test, build |
| `make test` | unit, property and end-to-end tests |
| `make race` | the same under the race detector |
| `make cover` | coverage summary |
| `make lint` | gofmt check and `go vet` |
| `make fuzz` | fuzz both parsers (`FUZZTIME=5m` to go longer) |
| `make parity` | byte-for-byte comparison against the Python reference |
| `make dist` | cross-compile release binaries |

## Two traps in the numeric core

These have both bitten already. `internal/quat`, `internal/correct/legacy.go`
and the sample-offset arithmetic are the affected code.

**Go fuses `a*b + c` into a single FMA instruction on arm64.** That rounds once
where CPython rounds twice, and the result diverges in the last bits — which,
after the final `float32` store, occasionally changes a written byte. Every
product in a sum needs an explicit conversion:

```go
total := float64(a[0] * b[0])   // not: total := a[0]*b[0]
total += float64(a[1] * b[1])
```

The conversion is the barrier the Go spec sanctions for exactly this. There is a
test that greps the compiled assembly for `FMADDD`; if you add arithmetic and it
starts failing, this is why.

**Python's `round()` breaks ties to even.** `math.Round` rounds half away from
zero. Filter radii are computed with it, so a `.5` boundary silently changes a
box-blur width. Use `quat.PyRound`.

## What tests are expected

A change to behaviour needs a test that fails without it. Beyond that:

- **Detection changes** need a synthetic fixture in `internal/detect`. Anything
  that makes detection more sensitive must keep `TestWhipPanProducesNoEvents`
  passing — a whip-pan is fast but *smooth* rotation, and smoothing it degrades
  footage that was fine. False positives cost more than misses here.
- **Anything touching writes** must keep the invariant tests in
  `cmd/djgyrofix/e2e_test.go` green. They assert that the file size never
  changes, that only `djmd` sample bytes are modified, and that every patch
  reverts byte for byte.
- **Parser changes** should be fuzzed (`make fuzz`). Both parsers walk length
  fields taken straight from the file.

## Style

Match the surrounding code. In practice that means:

- `gofmt`, no exceptions. `make lint` checks it.
- Full words for identifiers — `index`, not `i`; `quaternion`, not `q`. The
  existing code is consistent about this.
- Comments explain *why*, not what. A comment restating the code will be asked
  about in review; one explaining a non-obvious constraint is what the reviewer
  is looking for.
- Errors say what was expected and what was found, and name the file or offset.

## Cutting a release

The version is never edited in the source. `cmd/djgyrofix/version.go` resolves it
at run time, in descending order of authority:

1. **A link-time stamp** — `-ldflags "-X main.stamped=$(git describe --tags)"`,
   which both `make build` and the release workflow set.
2. **The module version** Go records for `go install <pkg>@<version>`, which
   carries no link flags but does know the tag it resolved.
3. **The VCS revision** Go embeds for a build from a working tree, reported as
   `devel+<commit>` or `devel+<commit>.dirty`.

A development build says so rather than claiming to be a release. That matters
because the version is written into every patch journal and is the first thing
quoted in a bug report.

So releasing is only a tag:

```bash
git tag -a v0.2.0 -m "djgyrofix 0.2.0"
git push origin v0.2.0
```

The release workflow then runs the full test suite and the golden parity gate,
cross-compiles all six targets, smoke-tests the linux binary end to end
(scan → fix → verify → revert must restore a byte-exact file), **asserts that
the binary reports the tag it was built from**, and publishes the archives with
a `SHA256SUMS` file.

Update `CHANGELOG.md` before tagging. Nothing else needs touching.

## Reporting a bug

Open an issue with the output of `djgyrofix info --all-variants FILE` attached.
That one command shows the track layout, the sniffed variant and what every
other variant path would have found, which answers most metadata questions
immediately.

**Please do not attach footage unless asked.** Files are gigabytes and usually
tell us less than the `info` output does.

If djgyrofix damaged a file, say so first — it is almost always recoverable. The
sidecar journal next to the video holds every original byte:

```bash
djgyrofix verify DJI_0042.MP4        # what is wrong with it
djgyrofix revert --force DJI_0042.MP4  # put it back
```

## Licence

djgyrofix is GPL-3.0-or-later, because it is a derivative work of a GPL-3.0
project. Contributions are accepted under the same licence.
