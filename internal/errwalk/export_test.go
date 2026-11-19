package errwalk

// List exposes the flattened entries of w to the black-box tests.
func (w *Walk) List() []error {
	return w.entries()
}
