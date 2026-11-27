package transcript

import (
	"context"
	"testing"
	"time"
)

func TestOMPReceiptIOCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	exited := make(chan struct{})
	result := make(chan bool, 1)
	t.Cleanup(func() {
		close(release)
		<-exited
		select {
		case ompReceiptIO <- struct{}{}:
			<-ompReceiptIO
		case <-time.After(time.Second):
			t.Error("completed I/O left the probe slot occupied")
		}
	})
	go func() {
		_, ok := runOMPReceiptIO(ctx, func() int {
			close(started)
			<-release
			close(exited)
			return 42
		})
		result <- ok
	}()
	<-started
	cancel()
	select {
	case ok := <-result:
		if ok {
			t.Fatal("cancelled observation reported a result")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled observation waited for stalled I/O")
	}
	if _, ok := runOMPReceiptIO(t.Context(), func() int { return 7 }); ok {
		t.Fatal("started another probe while the stalled worker owned the slot")
	}
}
