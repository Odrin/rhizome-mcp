package cli

import "errors"

// Exit codes are part of the CLI's contract with scripts and CI, so they are
// named here and documented in docs/05 section 14. Before this existed every
// failure collapsed to exit 1, which made a typo in a flag indistinguishable
// from a failed doctor check (ISSUE-207).
const (
	// ExitOK reports success.
	ExitOK = 0
	// ExitError reports a runtime or domain failure: the command was
	// well-formed but could not be completed.
	ExitError = 1
	// ExitUsage reports that the invocation itself was wrong — unknown
	// command, missing argument, unsupported flag value.
	ExitUsage = 2
	// ExitDoctorUnhealthy reports that `doctor` ran successfully and found
	// failed checks. It is distinct from ExitError so CI can tell "the health
	// check says the data is bad" from "the health check could not run".
	ExitDoctorUnhealthy = 3
)

// UsageError marks a failure caused by how the command was invoked. Package
// main maps it to ExitUsage.
type UsageError struct {
	message string
}

func (err *UsageError) Error() string {
	if err == nil || err.message == "" {
		return "usage error"
	}
	return err.message
}

// NewUsageError builds a usage failure with the given message.
func NewUsageError(message string) error {
	return &UsageError{message: message}
}

// DoctorUnhealthyError marks a doctor run that completed but reported failed
// checks. Package main maps it to ExitDoctorUnhealthy.
type DoctorUnhealthyError struct {
	// FailedChecks is the number of checks that reported unhealthy.
	FailedChecks int
}

func (err *DoctorUnhealthyError) Error() string {
	return "doctor found failed checks"
}

// NewDoctorUnhealthyError builds a doctor-unhealthy failure.
func NewDoctorUnhealthyError(failedChecks int) error {
	return &DoctorUnhealthyError{FailedChecks: failedChecks}
}

// ExitCodeFor maps an error returned by Run to the process exit code. It is
// the single place that decides, so main.go cannot drift from the CLI package.
func ExitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	var usageErr *UsageError
	if errors.As(err, &usageErr) {
		return ExitUsage
	}
	var doctorErr *DoctorUnhealthyError
	if errors.As(err, &doctorErr) {
		return ExitDoctorUnhealthy
	}
	return ExitError
}
