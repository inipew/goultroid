package core

import "time"

// PanicReport contains structured details about a recovered panic.
type PanicReport struct {
	Owner     string
	Component string
	Value     any
	Stack     []byte
	At        time.Time
}

// PanicReporter accepts structured reports of recovered panics.
type PanicReporter interface {
	ReportPanic(PanicReport)
}

// NoopPanicReporter discards panic reports.
type NoopPanicReporter struct{}

func (NoopPanicReporter) ReportPanic(PanicReport) {}
