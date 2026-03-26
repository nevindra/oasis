//go:build !linux

package ix

// hostMemoryBytes returns a fallback value on non-Linux platforms where
// syscall.Sysinfo is not available. Docker containers are only created
// on Linux, so this is only used in tests and cross-compilation.
func hostMemoryBytes() int64 {
	return 8 << 30 // fallback: 8 GB
}
