package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const imageTaskKeyPrefix = "image_task:"

// imageTaskUpdateAttempts bounds optimistic retries when another writer
// changed the task between WATCH and EXEC (heartbeat vs. finish vs. sweeper).
const imageTaskUpdateAttempts = 8

type imageTaskStore struct {
	rdb *redis.Client
}

func NewImageTaskStore(rdb *redis.Client) service.ImageTaskStore {
	return &imageTaskStore{rdb: rdb}
}

func (s *imageTaskStore) Save(ctx context.Context, task *service.ImageTaskRecord, ttl time.Duration) error {
	data, err := json.Marshal(task)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, imageTaskKey(task.ID), data, ttl).Err()
}

func (s *imageTaskStore) Get(ctx context.Context, id string) (*service.ImageTaskRecord, error) {
	data, err := s.rdb.Get(ctx, imageTaskKey(id)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, service.ErrImageTaskNotFound
		}
		return nil, err
	}
	var task service.ImageTaskRecord
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

// Update is a WATCH/MULTI read-modify-write: the write only commits if nobody
// touched the key since it was read, otherwise the mutation is re-applied to
// the fresh value.
func (s *imageTaskStore) Update(ctx context.Context, id string, ttl time.Duration, mutate func(*service.ImageTaskRecord) error) (*service.ImageTaskRecord, error) {
	key := imageTaskKey(id)
	var updated *service.ImageTaskRecord
	txf := func(tx *redis.Tx) error {
		data, err := tx.Get(ctx, key).Bytes()
		if err != nil {
			if err == redis.Nil {
				return service.ErrImageTaskNotFound
			}
			return err
		}
		var task service.ImageTaskRecord
		if err := json.Unmarshal(data, &task); err != nil {
			return err
		}
		if err := mutate(&task); err != nil {
			if errors.Is(err, service.ErrImageTaskUnchanged) {
				updated = &task
				return nil
			}
			return err
		}
		encoded, err := json.Marshal(&task)
		if err != nil {
			return err
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, key, encoded, ttl)
			return nil
		})
		if err == nil {
			updated = &task
		}
		return err
	}
	for attempt := 0; attempt < imageTaskUpdateAttempts; attempt++ {
		err := s.rdb.Watch(ctx, txf, key)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return updated, nil
	}
	return nil, redis.TxFailedErr
}

func (s *imageTaskStore) Delete(ctx context.Context, id string) error {
	return s.rdb.Del(ctx, imageTaskKey(id)).Err()
}

func imageTaskKey(id string) string {
	return imageTaskKeyPrefix + strings.TrimSpace(id)
}
