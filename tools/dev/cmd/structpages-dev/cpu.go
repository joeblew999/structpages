package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// cpuTime is the CPU a process has spent, user and system, as the operating
// system counts it: from /proc on Linux, in clock ticks of 10ms; from ps
// elsewhere, in hundredths of a second.
func cpuTime(pid int) (time.Duration, error) {
	if runtime.GOOS == "linux" {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return 0, err
		}
		// The fields after the command's name, which can hold spaces and
		// parentheses: utime and stime are the 12th and 13th of them.
		s := string(stat)
		fields := strings.Fields(s[strings.LastIndexByte(s, ')')+2:])
		if len(fields) < 13 {
			return 0, fmt.Errorf("/proc/%d/stat: too few fields", pid)
		}
		utime, _ := strconv.ParseInt(fields[11], 10, 64)
		stime, _ := strconv.ParseInt(fields[12], 10, 64)
		const ticks = 100 // USER_HZ, the same on every Linux
		return time.Duration(utime+stime) * time.Second / ticks, nil
	}
	// The server it started.
	out, err := exec.Command("ps", "-o", "cputime=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // see above
	if err != nil {
		return 0, fmt.Errorf("ps -p %d: %w", pid, err)
	}
	return parseCPUTime(strings.TrimSpace(string(out))), nil
}

// parseCPUTime reads ps's cputime: [[days-]hours:]minutes:seconds.
func parseCPUTime(s string) time.Duration {
	var d time.Duration
	if days, rest, ok := strings.Cut(s, "-"); ok {
		n, _ := strconv.Atoi(days)
		d, s = time.Duration(n)*24*time.Hour, rest
	}
	parts := strings.Split(s, ":")
	units := []time.Duration{time.Hour, time.Minute, time.Second}
	units = units[len(units)-len(parts):]
	for i, p := range parts {
		v, _ := strconv.ParseFloat(p, 64)
		d += time.Duration(v * float64(units[i]))
	}
	return d
}
