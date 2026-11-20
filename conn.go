//go:build !plan9

package prblm

import "syscall"

// brokenConnErrors are the errors that show the client connection is gone
// (EPIPE and ECONNRESET), so that nothing can be written to it.
var brokenConnErrors = []error{syscall.EPIPE, syscall.ECONNRESET}
