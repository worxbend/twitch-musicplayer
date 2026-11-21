package repository

import (
	"context"

	"lofi-radio-backend/internal/models"
)

// QueueRepository defines persistence operations for the playback queue.
type QueueRepository interface {
	Enqueue(ctx context.Context, item *models.QueueItem) error
	EnqueueFront(ctx context.Context, item *models.QueueItem) error
	Dequeue(ctx context.Context) (*models.QueueItem, error)
	List(ctx context.Context) ([]*models.QueueItem, error)
	Remove(ctx context.Context, id string) error
	// Move shifts an item by delta places, clamped to the queue bounds, and
	// returns its new 1-based position and whether it changed place.
	Move(ctx context.Context, id string, delta int) (position int, moved bool, err error)
	Clear(ctx context.Context) error
	Count(ctx context.Context) (int, error)
	RemoveBySongID(ctx context.Context, songID string) (int, error)
}
