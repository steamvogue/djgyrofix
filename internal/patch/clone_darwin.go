//go:build darwin

package patch

import (
	"os"
	"os/exec"
)

// cloneFile uses APFS clonefile, which makes a full backup copy instant and
// free — most of this tool's users are on APFS, so `--backup copy` is a real
// option there rather than a 20 GB duplication.
//
// clonefile(2) is only reachable through libSystem, which would mean cgo. The
// system cp implements it with -c, so shelling out keeps the build
// dependency-free and still gets the clone. A filesystem that cannot clone
// makes cp -c fail, and the caller falls back to a byte copy.
func cloneFile(source, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		return os.ErrExist
	}
	binary, err := exec.LookPath("cp")
	if err != nil {
		return errCloneUnsupported
	}
	if err := exec.Command(binary, "-c", source, destination).Run(); err != nil {
		os.Remove(destination)
		return errCloneUnsupported
	}
	return nil
}
