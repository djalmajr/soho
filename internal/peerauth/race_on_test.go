//go:build race

package peerauth_test

// raceDetector is true in a -race build: its instrumentation inflates heap
// allocation by a constant per header (measured about 19 KB per header, from
// 2,500 to 20,000 headers), so the allocation budget applies only without it.
const raceDetector = true
