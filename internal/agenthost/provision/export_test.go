package provision

import "time"

// SetPrepareLockWait changes how long Prepare waits for its lock, for a test;
// it returns what puts it back.
func SetPrepareLockWait(d time.Duration) (restore func()) {
	prev := prepareLockWait
	prepareLockWait = d
	return func() { prepareLockWait = prev }
}
