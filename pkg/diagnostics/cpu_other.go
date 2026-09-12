//go:build !linux && !windows

package diagnostics

import (
	"fmt"
	"time"
)

func ProcessCPUTime() (time.Duration, error) {
	return 0, fmt.Errorf("process CPU measurement is supported on Linux and Windows")
}
