## What this changes

<!-- One or two sentences. Link the issue if there is one. -->

## Why

<!-- What was wrong, or what this makes possible. -->

## Checklist

- [ ] `make lint test` passes.
- [ ] New behaviour has a test that fails without the change.
- [ ] `make parity` still passes, **or** this PR explains why the byte-for-byte
      match with the Python reference is expected to change.

## If this touches the numeric core

`internal/quat`, `internal/correct/legacy.go` and the sample-offset arithmetic
are held to byte-identical output against the Python reference. Two things bite
easily there:

- Every `a*b + c` needs an explicit `float64()` around the product. Go fuses it
  into a single FMA on arm64 otherwise, which rounds once instead of twice.
- Python's `round()` breaks ties to even; `math.Round` does not. Use
  `quat.PyRound`.

<!-- Delete this section if it does not apply. -->
