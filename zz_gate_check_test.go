package hjem

import "testing"

// Temporary: verifying the CI gate blocks the Docker push. Reverted next commit.
func TestCIGateDeliberateFailure(t *testing.T) {
	t.Fatal("deliberate failure to verify the Docker push is gated on tests")
}
