package quat_test

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// fusingArches are the Go targets where the compiler will contract a*b+c into a
// single fused multiply-add. On the others there is nothing to guard against,
// so the test has nothing to say.
var fusingArches = map[string]string{
	"arm64":   "FMADDD",
	"ppc64":   "FMADD",
	"ppc64le": "FMADD",
	"s390x":   "FMADD",
	"riscv64": "FMADDD",
	"loong64": "FMADDD",
}

// TestNumericCoreIsNotFused is the guard behind golden parity.
//
// Go permits fusing a*b+c into one instruction that rounds once instead of
// twice, and does so by default on several architectures. CPython always rounds
// twice. A fused multiply-add in this package therefore diverges from the
// reference implementation in the last bits of a float64 — which, often enough
// to matter, changes a byte after the final float32 store.
//
// The fix is an explicit float64() conversion around each product, which the Go
// spec defines as a rounding barrier. That is easy to forget when adding
// arithmetic, and the symptom appears far away as a parity failure against a
// Python tool. So the compiled output is checked directly.
func TestNumericCoreIsNotFused(t *testing.T) {
	instruction, fuses := fusingArches[runtime.GOARCH]
	if !fuses {
		t.Skipf("%s does not contract floating-point multiply-add", runtime.GOARCH)
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH to compile with")
	}

	output, err := exec.Command("go", "build", "-buildvcs=false", "-gcflags=-S",
		"github.com/steamvogue/djgyrofix/internal/quat").CombinedOutput()
	if err != nil {
		t.Fatalf("compiling for assembly inspection: %v\n%s", err, output)
	}
	if strings.Contains(string(output), instruction) {
		t.Errorf("internal/quat compiled to a fused multiply-add (%s) on %s.\n"+
			"That rounds once where CPython rounds twice and breaks byte parity with the "+
			"Python reference. Wrap the product in an explicit float64(), as the rest of "+
			"the package does: total += float64(a[i] * b[i]).", instruction, runtime.GOARCH)
	}
}
