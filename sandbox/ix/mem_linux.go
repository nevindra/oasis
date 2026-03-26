package ix

import "syscall"

// hostMemoryBytes returns the total system memory in bytes using sysinfo(2).
func hostMemoryBytes() int64 {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return 4 << 30 // fallback: 4 GB
	}
	return int64(info.Totalram) * int64(info.Unit)
}
