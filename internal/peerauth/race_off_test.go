//go:build !race

package peerauth_test

// raceDetector is false without -race; see race_on_test.go.
const raceDetector = false
