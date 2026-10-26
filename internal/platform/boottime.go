package platform

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// BootTime is injectable; an error means the machine's boot time is unknown.
var BootTime = bootTime

var darwinBootPattern = regexp.MustCompile(`\{\s*sec\s*=\s*(\d+)\s*,\s*usec\s*=\s*(\d+)\s*\}`)

func parseDarwinBootTime(raw string) (time.Time, error) {
	fields := darwinBootPattern.FindStringSubmatch(raw)
	if len(fields) != 3 {
		return time.Time{}, fmt.Errorf("invalid kern.boottime")
	}
	sec, e := strconv.ParseInt(fields[1], 10, 64)
	if e != nil {
		return time.Time{}, e
	}
	micro, e := strconv.ParseInt(fields[2], 10, 64)
	if e != nil || micro >= 1000000 {
		return time.Time{}, fmt.Errorf("invalid boot microseconds")
	}
	return time.Unix(sec, micro*1000), nil
}
func parseLinuxBootTime(raw string) (time.Time, error) {
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "btime" {
			if len(fields) != 2 {
				return time.Time{}, fmt.Errorf("invalid btime")
			}
			sec, e := strconv.ParseInt(fields[1], 10, 64)
			if e != nil || sec <= 0 {
				return time.Time{}, fmt.Errorf("invalid btime")
			}
			return time.Unix(sec, 0), nil
		}
	}
	return time.Time{}, fmt.Errorf("btime missing")
}
