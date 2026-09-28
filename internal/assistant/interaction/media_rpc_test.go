package interaction

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	assistentrpc "github.com/inipew/goultroid/internal/assistant/rpc"
)

type recordedMediaRPC struct {
	method string
	family string
	kind   assistentrpc.Kind
}

type recordingMediaExecutor struct {
	calls []recordedMediaRPC
	err   error
}

func (e *recordingMediaExecutor) Do(
	ctx context.Context,
	method, family string,
	kind assistentrpc.Kind,
	_ time.Duration,
	operation func(context.Context) error,
) error {
	e.calls = append(e.calls, recordedMediaRPC{
		method: method,
		family: family,
		kind:   kind,
	})
	if e.err != nil {
		return e.err
	}
	return operation(ctx)
}

type recordingUploadClient struct {
	small int
	big   int
}

func (c *recordingUploadClient) UploadSaveFilePart(
	context.Context,
	*tg.UploadSaveFilePartRequest,
) (bool, error) {
	c.small++
	return true, nil
}

func (c *recordingUploadClient) UploadSaveBigFilePart(
	context.Context,
	*tg.UploadSaveBigFilePartRequest,
) (bool, error) {
	c.big++
	return true, nil
}

func TestManagedUploadRPCClientAccountsEveryPhysicalPart(t *testing.T) {
	raw := &recordingUploadClient{}
	executor := &recordingMediaExecutor{}
	client := &managedUploadRPCClient{
		raw: raw,
		executor: func() assistentrpc.Executor {
			return executor
		},
	}

	for part := 0; part < 3; part++ {
		ok, err := client.UploadSaveFilePart(context.Background(), &tg.UploadSaveFilePartRequest{
			FileID:   11,
			FilePart: part,
			Bytes:    []byte{byte(part)},
		})
		if err != nil || !ok {
			t.Fatalf("UploadSaveFilePart(%d) = %v, %v", part, ok, err)
		}
	}
	for part := 0; part < 2; part++ {
		ok, err := client.UploadSaveBigFilePart(context.Background(), &tg.UploadSaveBigFilePartRequest{
			FileID:         22,
			FilePart:       part,
			FileTotalParts: 2,
			Bytes:          []byte{byte(part)},
		})
		if err != nil || !ok {
			t.Fatalf("UploadSaveBigFilePart(%d) = %v, %v", part, ok, err)
		}
	}

	if raw.small != 3 || raw.big != 2 {
		t.Fatalf("physical upload calls small/big=%d/%d, want 3/2", raw.small, raw.big)
	}
	if len(executor.calls) != 5 {
		t.Fatalf("executor calls=%d, want one per physical upload part", len(executor.calls))
	}
	for i, call := range executor.calls {
		wantMethod := "upload.saveFilePart"
		if i >= 3 {
			wantMethod = "upload.saveBigFilePart"
		}
		if call.method != wantMethod || call.family != "upload" ||
			call.kind != assistentrpc.IdempotentMutation {
			t.Fatalf("executor call[%d]=%+v, want method=%q family=upload kind=idempotent", i, call, wantMethod)
		}
	}
}

func TestManagedUploadRPCClientHidesExecutorCauseFromUploaderRetryPolicy(t *testing.T) {
	sentinel := errors.New("executor decided upload failure")
	raw := &recordingUploadClient{}
	executor := &recordingMediaExecutor{err: sentinel}
	client := &managedUploadRPCClient{
		raw: raw,
		executor: func() assistentrpc.Executor {
			return executor
		},
	}

	_, err := client.UploadSaveFilePart(context.Background(), &tg.UploadSaveFilePartRequest{
		FileID: 1,
		Bytes:  []byte{1},
	})
	if err == nil {
		t.Fatal("UploadSaveFilePart() error=nil, want executor failure")
	}
	if errors.Is(err, sentinel) {
		t.Fatal("physical boundary exposed executor cause to gotd uploader retry classification")
	}
	if got := unwrapMediaRPCBoundary(err); got != sentinel {
		t.Fatalf("unwrapMediaRPCBoundary()=%v, want sentinel", got)
	}
	if raw.small != 0 {
		t.Fatalf("raw upload calls=%d, want 0 when executor rejects before operation", raw.small)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("executor calls=%d, want 1", len(executor.calls))
	}
}
