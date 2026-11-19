package prblm

// brokenConnErrors is empty on Plan 9, which has no EPIPE or ECONNRESET
// errors to recognize: a write to a client that is gone fails quietly
// instead.
var brokenConnErrors []error
