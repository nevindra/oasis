package guardrail

import "log/slog"

// nopLogger drops all log records. Used as the default logger for
// guards constructed without an explicit InjectionLogger / ContentLogger /
// WithKeywordLogger option. Replicated locally from the root package's
// agent.go:697 so this module does not depend on internal root types.
var nopLogger = slog.New(slog.DiscardHandler)
