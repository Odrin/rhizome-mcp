//go:build integration && !race

package integration_test

// integrationRaceEnabled reports whether this test binary was built with the
// race detector. See race_enabled_test.go for why the server build follows it.
const integrationRaceEnabled = false
