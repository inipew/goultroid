package download

import (
	"strconv"
	"strings"
	"sync"
)

const (
	extractorProgressPrefix     = "goultroid-progress:"
	extractorProgressMaxPending = 8 * 1024
	extractorProgressTemplate   = "download:" + extractorProgressPrefix + "%(progress.downloaded_bytes)s\t%(progress.total_bytes)s\t%(progress.total_bytes_estimate)s"
)

type extractorProgressObserver struct {
	mu      sync.Mutex
	pending string
	cb      ProgressCallback
}

func newExtractorProgressObserver(cb ProgressCallback) *extractorProgressObserver {
	if cb == nil {
		return nil
	}
	return &extractorProgressObserver{cb: cb}
}

func (o *extractorProgressObserver) Observe(chunk []byte) {
	if o == nil || o.cb == nil || len(chunk) == 0 {
		return
	}

	text := strings.ReplaceAll(string(chunk), "\r", "\n")
	updates := make([][2]int64, 0, 2)

	o.mu.Lock()
	o.pending += text
	if len(o.pending) > extractorProgressMaxPending {
		o.pending = o.pending[len(o.pending)-extractorProgressMaxPending:]
	}
	for {
		newline := strings.IndexByte(o.pending, '\n')
		if newline < 0 {
			break
		}
		line := o.pending[:newline]
		o.pending = o.pending[newline+1:]
		downloaded, total, ok := parseExtractorProgressLine(line)
		if ok {
			updates = append(updates, [2]int64{downloaded, total})
		}
	}
	o.mu.Unlock()

	for _, update := range updates {
		o.cb(update[0], update[1])
	}
}

func parseExtractorProgressLine(line string) (downloaded, total int64, ok bool) {
	line = strings.TrimSpace(line)
	prefix := strings.Index(line, extractorProgressPrefix)
	if prefix < 0 {
		return 0, 0, false
	}
	fields := strings.Split(strings.TrimSpace(line[prefix+len(extractorProgressPrefix):]), "\t")
	if len(fields) < 3 {
		return 0, 0, false
	}

	downloaded, ok = parseExtractorProgressNumber(fields[0])
	if !ok || downloaded < 0 {
		return 0, 0, false
	}
	if parsed, valid := parseExtractorProgressNumber(fields[1]); valid && parsed > 0 {
		total = parsed
	} else if parsed, valid := parseExtractorProgressNumber(fields[2]); valid && parsed > 0 {
		total = parsed
	}
	return downloaded, total, true
}

func parseExtractorProgressNumber(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "NA") || strings.EqualFold(raw, "none") {
		return 0, false
	}
	if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return value, true
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 {
		return 0, false
	}
	return int64(value), true
}
