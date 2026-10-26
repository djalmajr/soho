package platform

import (
	"fmt"
	"strconv"
	"syscall"
	"time"
)

var bootTicks = syscall.NewLazyDLL("kernel32.dll").NewProc("GetTickCount64")

func bootTime() (time.Time, error) {
	if err := bootTicks.Find(); err != nil {
		return time.Time{}, err
	}
	low, high, _ := bootTicks.Call()
	ticks := uint64(low)
	if strconv.IntSize == 32 {
		ticks |= uint64(high) << 32
	}
	if ticks > uint64((1<<63-1)/int64(time.Millisecond)) {
		return time.Time{}, fmt.Errorf("invalid boot tick count")
	}
	return time.Now().Add(-time.Duration(ticks) * time.Millisecond), nil
}
