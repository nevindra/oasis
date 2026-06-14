package core

// Ptr returns a pointer to v. It is a convenience for setting optional pointer
// fields (e.g. Generation.Temperature *float64) from a literal:
//
//	Generation{Temperature: core.Ptr(0.2)}
func Ptr[T any](v T) *T { return &v }
