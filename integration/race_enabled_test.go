//go:build integration && race

package integration_test

// integrationRaceEnabled reports whether this test binary was built with the
// race detector, so TestMain can build the server binary the same way: a
// race-instrumented harness driving an uninstrumented server would report a
// clean run while the server's own goroutines went unchecked (ISSUE-210 AC1).
const integrationRaceEnabled = true
