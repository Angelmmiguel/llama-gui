//go:build !linux

package reaper

import (
	"context"
	"log/slog"
)

// Reap does nothing: being PID 1 is a Linux container's concern.
func Reap(context.Context, *slog.Logger) {}
