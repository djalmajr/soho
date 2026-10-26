package platform

import (
	"context"
	"os/exec"
	"time"
)

func bootTime() (time.Time, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "/usr/sbin/sysctl", "-n", "kern.boottime").Output()
	if err != nil {
		return time.Time{}, err
	}
	return parseDarwinBootTime(string(data))
}
