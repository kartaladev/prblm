package prblm

import "context"

// ErrTooLarge exposes errTooLarge to the black-box tests.
var ErrTooLarge = errTooLarge

// IsReporting exposes h.isReporting to the black-box tests.
func IsReporting(ctx context.Context, h *Handler) bool {
	return h.isReporting(ctx)
}
