package telegram

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

const mediaTransferTimeout = 30 * time.Minute

// mediaRPCBoundaryError deliberately does not implement Unwrap. gotd's media
// helpers contain their own FloodWait/timeout retry loops; exposing the final
// Telegram/network cause to those loops would create a second retry policy
// after RPCExecutor has already made the retry/defer decision.
type mediaRPCBoundaryError struct {
	cause error
}

func (e *mediaRPCBoundaryError) Error() string {
	if e == nil || e.cause == nil {
		return "media rpc failed"
	}
	return e.cause.Error()
}

func unwrapMediaRPCBoundary(err error) error {
	if err == nil {
		return nil
	}
	var boundary *mediaRPCBoundaryError
	if errors.As(err, &boundary) && boundary != nil && boundary.cause != nil {
		return boundary.cause
	}
	return err
}

func executePhysicalMediaRPC[T any](
	ctx context.Context,
	executor func() *RPCExecutor,
	method string,
	kind RPCOperationKind,
	op func(context.Context) (T, error),
) (T, error) {
	var zero T
	if executor == nil {
		return zero, &mediaRPCBoundaryError{cause: errors.New("media rpc executor is unavailable")}
	}
	exec := executor()
	if exec == nil {
		return zero, &mediaRPCBoundaryError{cause: errors.New("media rpc executor is unavailable")}
	}

	value, err := ExecuteRPC(ctx, exec, RPCMeta{
		Method: method,
		Family: "upload",
		Kind:   kind,
	}, op)
	if err == nil {
		return value, nil
	}

	cause := err
	var failure *RPCFailure
	if errors.As(err, &failure) && failure != nil && failure.Err != nil {
		cause = failure.Err
	}
	mapped := mapTelegramError(cause)
	if mapped == nil {
		mapped = cause
	}
	return zero, &mediaRPCBoundaryError{cause: mapped}
}

// managedUploadRPCClient places the limiter/retry/metrics boundary around each
// physical upload part rather than around the whole file transfer.
type managedUploadRPCClient struct {
	raw      uploader.Client
	executor func() *RPCExecutor
}

var _ uploader.Client = (*managedUploadRPCClient)(nil)

func (c *managedUploadRPCClient) UploadSaveFilePart(ctx context.Context, req *tg.UploadSaveFilePartRequest) (bool, error) {
	if c == nil || c.raw == nil {
		return false, &mediaRPCBoundaryError{cause: errors.New("telegram upload client is unavailable")}
	}
	return executePhysicalMediaRPC(ctx, c.executor, "upload.saveFilePart", RPCIdempotentMutation, func(opCtx context.Context) (bool, error) {
		return c.raw.UploadSaveFilePart(opCtx, req)
	})
}

func (c *managedUploadRPCClient) UploadSaveBigFilePart(ctx context.Context, req *tg.UploadSaveBigFilePartRequest) (bool, error) {
	if c == nil || c.raw == nil {
		return false, &mediaRPCBoundaryError{cause: errors.New("telegram upload client is unavailable")}
	}
	return executePhysicalMediaRPC(ctx, c.executor, "upload.saveBigFilePart", RPCIdempotentMutation, func(opCtx context.Context) (bool, error) {
		return c.raw.UploadSaveBigFilePart(opCtx, req)
	})
}

// managedDownloadRPCClient does the same for downloader chunk/hash protocol
// calls. Keeping all downloader.Client methods here also prevents future CDN or
// verification paths from silently bypassing the shared RPC policy.
type managedDownloadRPCClient struct {
	raw      downloader.Client
	executor func() *RPCExecutor
}

var _ downloader.Client = (*managedDownloadRPCClient)(nil)

func (c *managedDownloadRPCClient) UploadGetFile(ctx context.Context, req *tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
	if c == nil || c.raw == nil {
		return nil, &mediaRPCBoundaryError{cause: errors.New("telegram download client is unavailable")}
	}
	return executePhysicalMediaRPC(ctx, c.executor, "upload.getFile", RPCReadOnly, func(opCtx context.Context) (tg.UploadFileClass, error) {
		return c.raw.UploadGetFile(opCtx, req)
	})
}

func (c *managedDownloadRPCClient) UploadGetFileHashes(ctx context.Context, req *tg.UploadGetFileHashesRequest) ([]tg.FileHash, error) {
	if c == nil || c.raw == nil {
		return nil, &mediaRPCBoundaryError{cause: errors.New("telegram download client is unavailable")}
	}
	return executePhysicalMediaRPC(ctx, c.executor, "upload.getFileHashes", RPCReadOnly, func(opCtx context.Context) ([]tg.FileHash, error) {
		return c.raw.UploadGetFileHashes(opCtx, req)
	})
}

func (c *managedDownloadRPCClient) UploadReuploadCDNFile(ctx context.Context, req *tg.UploadReuploadCDNFileRequest) ([]tg.FileHash, error) {
	if c == nil || c.raw == nil {
		return nil, &mediaRPCBoundaryError{cause: errors.New("telegram download client is unavailable")}
	}
	return executePhysicalMediaRPC(ctx, c.executor, "upload.reuploadCdnFile", RPCIdempotentMutation, func(opCtx context.Context) ([]tg.FileHash, error) {
		return c.raw.UploadReuploadCDNFile(opCtx, req)
	})
}

func (c *managedDownloadRPCClient) UploadGetCDNFileHashes(ctx context.Context, req *tg.UploadGetCDNFileHashesRequest) ([]tg.FileHash, error) {
	if c == nil || c.raw == nil {
		return nil, &mediaRPCBoundaryError{cause: errors.New("telegram download client is unavailable")}
	}
	return executePhysicalMediaRPC(ctx, c.executor, "upload.getCdnFileHashes", RPCReadOnly, func(opCtx context.Context) ([]tg.FileHash, error) {
		return c.raw.UploadGetCDNFileHashes(opCtx, req)
	})
}

func (c *managedDownloadRPCClient) UploadGetWebFile(ctx context.Context, req *tg.UploadGetWebFileRequest) (*tg.UploadWebFile, error) {
	if c == nil || c.raw == nil {
		return nil, &mediaRPCBoundaryError{cause: errors.New("telegram download client is unavailable")}
	}
	return executePhysicalMediaRPC(ctx, c.executor, "upload.getWebFile", RPCReadOnly, func(opCtx context.Context) (*tg.UploadWebFile, error) {
		return c.raw.UploadGetWebFile(opCtx, req)
	})
}

func (e *mediaRPCBoundaryError) Format(s fmt.State, verb rune) {
	if e == nil || e.cause == nil {
		fmt.Fprint(s, "media rpc failed")
		return
	}
	fmt.Fprint(s, e.cause.Error())
}
