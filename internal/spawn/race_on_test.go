//go:build race

package spawn

// raceDetector is true in a -race build: one fake hc call can then outlast a
// short startup deadline, so tests that need a read before the deadline skip.
const raceDetector = true
