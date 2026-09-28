package interaction

import (
	"context"
	"errors"
	"time"

	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	assistentrpc "github.com/inipew/goultroid/internal/assistant/rpc"
)

const mediaTransferTimeout = 30 * time.Minute

// mediaRPCBoundaryError deliberately does not implement Unwrap. gotd's
// uploader contains its own retry behavior; exposing the executor's final
// Telegram/network cause would let the helper apply another retry policy after
// the shared executor has already made that decision.
type mediaRPCBoundaryError struct {
	cause error
}

func (e *mediaRPCBoundaryError) Error() string {
	if e == nil || e.cause == nil {
		return "assistant media rpc failed"
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

type managedUploadRPCClient struct {
	raw      uploader.Client
	executor func() assistentrpc.Executor
}

var _ uploader.Client = (*managedUploadRPCClient)(nil)

func (c *managedUploadRPCClient) executorNow() assistentrpc.Executor {
	if c == nil || c.executor == nil {
		return nil
	}
	return c.executor()
}

func (c *managedUploadRPCClient) executeBool(
	ctx context.Context,
	method string,
	op func(context.Context) (bool, error),
) (bool, error) {
	if c == nil || c.raw == nil || op == nil {
		return false, &mediaRPCBoundaryError{cause: errors.New("assistant upload client is unavailable")}
	}
	executor := c.executorNow()
	if executor == nil {
		return false, &mediaRPCBoundaryError{cause: errors.New("assistant media rpc executor is unavailable")}
	}

	var value bool
	err := executor.Do(
		ctx,
		method,
		"upload",
		assistentrpc.IdempotentMutation,
		mediaTransferTimeout,
		func(opCtx context.Context) error {
			var opErr error
			value, opErr = op(opCtx)
			return opErr
		},
	)
	if err != nil {
		return false, &mediaRPCBoundaryError{cause: err}
	}
	return value, nil
}

func (c *managedUploadRPCClient) UploadSaveFilePart(
	ctx context.Context,
	req *tg.UploadSaveFilePartRequest,
) (bool, error) {
	return c.executeBool(ctx, "upload.saveFilePart", func(opCtx context.Context) (bool, error) {
		return c.raw.UploadSaveFilePart(opCtx, req)
	})
}

func (c *managedUploadRPCClient) UploadSaveBigFilePart(
	ctx context.Context,
	req *tg.UploadSaveBigFilePartRequest,
) (bool, error) {
	return c.executeBool(ctx, "upload.saveBigFilePart", func(opCtx context.Context) (bool, error) {
		return c.raw.UploadSaveBigFilePart(opCtx, req)
	})
}

func newManagedMediaUploader(
	raw uploader.Client,
	executor func() assistentrpc.Executor,
) MediaUploader {
	if raw == nil || executor == nil {
		return nil
	}
	return uploader.NewUploader(&managedUploadRPCClient{
		raw:      raw,
		executor: executor,
	})
}

func (c *ClientInteraction) uploadMediaFile(
	ctx context.Context,
	filePath string,
) (tg.InputFileClass, error) {
	if c == nil || c.uploader == nil {
		return nil, errors.New("assistant media upload is not configured")
	}
	uploadCtx, cancel := context.WithTimeout(ctx, mediaTransferTimeout)
	defer cancel()

	inputFile, err := c.uploader.FromPath(uploadCtx, filePath)
	if err != nil {
		return nil, unwrapMediaRPCBoundary(err)
	}
	return inputFile, nil
}
