//go:build !linux

package app

func readRSSBytes() (uint64, bool) { return 0, false }
