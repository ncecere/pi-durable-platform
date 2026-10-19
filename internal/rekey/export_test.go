package rekey

// SetAfterBatchRead installs the hook and returns what restores it. Tests
// using it must not run in parallel.
func SetAfterBatchRead(f func(column string)) (restore func()) {
	afterBatchRead = f
	return func() { afterBatchRead = nil }
}

// SealedColumns lists the columns a run re-seals, as table.column.
func SealedColumns() []string {
	var out []string
	for _, c := range columns {
		out = append(out, c.label())
	}
	return out
}
