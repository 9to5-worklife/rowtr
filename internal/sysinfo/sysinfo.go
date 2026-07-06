// Package sysinfo detects the host's basic capabilities so setup can recommend
// an appropriately sized local model.
package sysinfo

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// Info is a coarse snapshot of the machine.
type Info struct {
	OS           string
	Arch         string
	TotalRAMGB   float64 // 0 if it couldn't be read
	AppleSilicon bool
}

// Detect gathers system info. It never errors — unknown fields are left zero so
// the recommender can fall back to a safe default.
func Detect() Info {
	ram, _ := totalRAMBytes()
	return Info{
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		TotalRAMGB:   float64(ram) / (1 << 30),
		AppleSilicon: runtime.GOOS == "darwin" && runtime.GOARCH == "arm64",
	}
}

func totalRAMBytes() (uint64, error) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0, err
		}
		return strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	case "linux":
		f, err := os.Open("/proc/meminfo")
		if err != nil {
			return 0, err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "MemTotal:") {
				fields := strings.Fields(sc.Text())
				if len(fields) >= 2 {
					kb, err := strconv.ParseUint(fields[1], 10, 64)
					if err != nil {
						return 0, err
					}
					return kb * 1024, nil // /proc/meminfo is in kB
				}
			}
		}
		return 0, sc.Err()
	default:
		return 0, nil // unsupported OS → 0, recommender uses the safe default
	}
}
