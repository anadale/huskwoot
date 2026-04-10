package api

// SetSSEReplayBatchSizeForTest replaces sseReplayBatchSize and returns a
// restore function. The _test.go suffix means this helper is only compiled
// during testing and is invisible to the production binary, yet visible to
// the external package api_test.
func SetSSEReplayBatchSizeForTest(n int) func() {
	old := sseReplayBatchSize
	sseReplayBatchSize = n
	return func() { sseReplayBatchSize = old }
}
