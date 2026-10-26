package platform

import (
	"testing"
	"time"
)

func TestBootTimeParsers(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		parser    func(string) (time.Time, error)
		sec       int64
		micro     int
		bad       bool
	}{
		{name: "macOS", raw: "{ sec = 1792987200, usec = 12345 } Mon Oct 26", parser: parseDarwinBootTime, sec: 1792987200, micro: 12345},
		{name: "Linux", raw: "cpu 1 2 3\nbtime 1792987200\nprocesses 9\n", parser: parseLinuxBootTime, sec: 1792987200},
		{name: "macOS-garbage", raw: "garbage", parser: parseDarwinBootTime, bad: true},
		{name: "macOS-invalid-usec", raw: "{ sec = 10, usec = 1000000 }", parser: parseDarwinBootTime, bad: true},
		{name: "Linux-garbage", raw: "btime garbage", parser: parseLinuxBootTime, bad: true},
		{name: "Linux-missing", raw: "cpu 1 2", parser: parseLinuxBootTime, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.parser(tc.raw)
			if tc.bad {
				if err == nil {
					t.Fatalf("accepted garbage %q", tc.raw)
				}
				return
			}
			if err != nil || got.Unix() != tc.sec || got.Nanosecond() != tc.micro*1000 {
				t.Fatalf("got=%s err=%v", got, err)
			}
			t.Logf("boot=%s", got)
		})
	}
}
