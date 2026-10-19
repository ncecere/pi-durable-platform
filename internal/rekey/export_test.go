package rekey

// SetAfterBatchRead installs the hook and returns what restores it. Tests
// using it must not run in parallel.
func SetAfterBatchRead(f func(column string)) (restore func()) {
	afterBatchRead = f
	return func() { afterBatchRead = nil }
}
