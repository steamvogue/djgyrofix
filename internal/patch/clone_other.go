//go:build !darwin

package patch

// cloneFile has no dependency-free clone path outside macOS.
//
// On Linux the fallback is not a loss: io.Copy between two files goes through
// copy_file_range, which the kernel already turns into a reflink on btrfs and
// XFS. On Windows it is a plain copy.
func cloneFile(source, destination string) error { return errCloneUnsupported }
