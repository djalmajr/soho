package platform

import (
	"os"
	"time"
)

func bootTime() (time.Time, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	return parseLinuxBootTime(string(data))
}
