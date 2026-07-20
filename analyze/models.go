package analyze

// TestState represents the state of a test in a specific run.
type TestState int8

const (
	TestNotPresent TestState = -1 // test didn't exist in this run
	TestPassed     TestState = 0  // test ran and passed
	TestFailed     TestState = 1  // test ran and failed
)
