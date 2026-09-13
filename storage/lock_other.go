//go:build !darwin && !linux

package storage

import (
	"fmt"
	"os"
)

func acquireLock(path string) (*os.File, error) {
	return nil, fmt.Errorf("persistent nodes currently require macOS or Linux")
}
