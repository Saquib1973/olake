package base

import (
	"context"
	"fmt"
	"time"

	"github.com/datazip-inc/olake/protocol"
	"github.com/datazip-inc/olake/types"
	"github.com/datazip-inc/olake/utils"
	"github.com/datazip-inc/olake/utils/logger"
)

func ProcessBackfill(
	ctx context.Context,
	pool *protocol.WriterPool,
	stream protocol.Stream,
	fetchChunks func(ctx context.Context, stream protocol.Stream, pool *protocol.WriterPool) ([]types.Chunk, error),
	iterator func(context.Context, types.Chunk, protocol.Stream, func(types.RawRecord) error) error,
	cleaner func(context.Context, protocol.Stream, types.Chunk),
	retryCount int,
) error {
	chunks, err := fetchChunks(ctx, stream, pool)
	if err != nil {
		return fmt.Errorf("failed to fetch chunks: %w", err)
	}
	processChunk := func(ctx context.Context, chunk types.Chunk) error {
		start := time.Now()
		defer cleaner(ctx, stream, chunk)

		err := RetryOnBackoff(retryCount, 1*time.Minute, func() error {
			// Writer thread management
			errChan := make(chan error, 1)
			writer, err := pool.NewThread(ctx, stream,
				protocol.WithErrorChannel(errChan),
				protocol.WithBackfill(true),
			)
			if err != nil {
				return fmt.Errorf("writer init failed: %w", err)
			}
			defer writer.Close()

			// Database-specific data iteration
			if err := iterator(ctx, chunk, stream, func(r types.RawRecord) error {
				return writer.Insert(r)
			}); err != nil {
				return fmt.Errorf("iteration failed: %w", err)
			}

			// Final flush and error check
			return <-errChan
		})

		if err == nil {
			logger.Infof("Chunk %v-%v completed in %.2fs",
				chunk.Min, chunk.Max, time.Since(start).Seconds())
		}
		return err
	}

	utils.ConcurrentInGroup(protocol.GlobalConnGroup, chunks, processChunk)
	return nil
}
