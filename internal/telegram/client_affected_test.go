package telegram

import (
	"context"
	"testing"
)

type recordingAffectedHandler struct {
	channelID int64
	pts       int
	ptsCount  int
}

func (h *recordingAffectedHandler) HandleAffected(_ context.Context, channelID int64, pts, ptsCount int) error {
	h.channelID = channelID
	h.pts = pts
	h.ptsCount = ptsCount
	return nil
}

func TestAffectedUpdateHandlerRequiresBoundDelegate(t *testing.T) {
	bridge := &affectedUpdateHandler{}
	if err := bridge.HandleAffected(context.Background(), 1, 2, 3); err == nil {
		t.Fatal("HandleAffected() before binding returned nil error")
	}
}

func TestAffectedUpdateHandlerDelegates(t *testing.T) {
	recorder := &recordingAffectedHandler{}
	bridge := &affectedUpdateHandler{delegate: recorder}
	if err := bridge.HandleAffected(context.Background(), 11, 22, 33); err != nil {
		t.Fatalf("HandleAffected() error = %v", err)
	}
	if recorder.channelID != 11 || recorder.pts != 22 || recorder.ptsCount != 33 {
		t.Fatalf("delegated affected = (%d,%d,%d), want (11,22,33)", recorder.channelID, recorder.pts, recorder.ptsCount)
	}
}
