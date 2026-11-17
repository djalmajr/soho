//go:build !race

package spawn

// raceDetector is false without -race; see race_on_test.go.
const raceDetector = false
