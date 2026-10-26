//go:build !darwin && !linux && !windows

package platform

import (
	"fmt"
	"time"
)

func bootTime() (time.Time, error) {
	return time.Time{}, fmt.Errorf("boot time unavailable on this platform")
}
