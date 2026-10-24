package bench

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// PeakRSSMB returns the high-water resident set of a process in MiB from
// /proc (Linux). Other systems return ok=false and the report shows "n/a"
// rather than a made-up number.
func PeakRSSMB(pid int) (mb float64, ok bool) {
	if pid <= 0 {
		return 0, false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, false
	}
	return parseVmHWM(string(b))
}

func parseVmHWM(status string) (float64, bool) {
	for _, line := range strings.Split(status, "\n") {
		v, found := strings.CutPrefix(line, "VmHWM:")
		if !found {
			continue
		}
		f := strings.Fields(v) // "12345 kB"
		if len(f) < 1 {
			return 0, false
		}
		kb, err := strconv.ParseFloat(f[0], 64)
		if err != nil {
			return 0, false
		}
		return kb / 1024, true
	}
	return 0, false
}
