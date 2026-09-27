//go:build linux

package app

import (
	"os"
	"strconv"
	"strings"
)

func readRSSBytes() (uint64, bool) {
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	return parseRSSStatm(raw, uint64(os.Getpagesize()))
}

func parseRSSStatm(raw []byte, pageSize uint64) (uint64, bool) {
	fields := strings.Fields(string(raw))
	if len(fields) < 2 || pageSize == 0 {
		return 0, false
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil || pages == 0 || pages > ^uint64(0)/pageSize {
		return 0, false
	}
	return pages * pageSize, true
}
