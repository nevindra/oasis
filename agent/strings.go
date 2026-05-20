package agent

// TruncateStr truncates a string to n runes.
// Exported for network subpackage access.
func TruncateStr(s string, n int) string {
	// Fast path: byte length ≤ n guarantees rune count ≤ n,
	// avoiding the []rune allocation for short/ASCII strings.
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
