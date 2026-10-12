package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Redis layout of the async image task coordinator (all keys expire):
//
//	image_task_idem:<scope>           JSON {fingerprint, task_id}
//	image_task:inflight               ZSET "<api_key_id>:<task_id>" -> heartbeat ms (sweeper index)
//	image_task:inflight:<api_key_id>  ZSET "<task_id>" -> heartbeat ms (per-key admission)
//	image_task:lock:<name>            SET NX lock
//	image_task:poll:<api_key_id>:<s>  per-second poll counter
const (
	imageTaskIdempotencyPrefix = "image_task_idem:"
	imageTaskInFlightKey       = "image_task:inflight"
	imageTaskInFlightKeyPrefix = "image_task:inflight:"
	imageTaskLockPrefix        = "image_task:lock:"
	imageTaskPollPrefix        = "image_task:poll:"
	// The per-key index only needs to outlive its longest task.
	imageTaskInFlightIndexTTL = 2 * time.Hour
)

// admitImageTaskScript adds one task to both in-flight indexes unless the key
// already has `limit` tasks whose heartbeat is at or after `live_since`.
//
//	KEYS[1] per-key zset, KEYS[2] global zset
//	ARGV: live_since_ms, limit, now_ms, task_id, global_member, index_ttl_s
var admitImageTaskScript = redis.NewScript(`
local limit = tonumber(ARGV[2])
if limit > 0 then
  local live = redis.call('ZCOUNT', KEYS[1], ARGV[1], '+inf')
  if live >= limit then
    return 0
  end
end
redis.call('ZADD', KEYS[1], ARGV[3], ARGV[4])
redis.call('EXPIRE', KEYS[1], ARGV[6])
redis.call('ZADD', KEYS[2], ARGV[3], ARGV[5])
return 1
`)

// touchImageTaskScript refreshes the heartbeat score only for members that are
// still indexed (XX), so a heartbeat racing a release cannot resurrect it.
var touchImageTaskScript = redis.NewScript(`
redis.call('ZADD', KEYS[1], 'XX', ARGV[1], ARGV[2])
redis.call('ZADD', KEYS[2], 'XX', ARGV[1], ARGV[3])
return 1
`)

type imageTaskCoordinator struct {
	rdb *redis.Client
}

func NewImageTaskCoordinator(rdb *redis.Client) service.ImageTaskCoordinator {
	return &imageTaskCoordinator{rdb: rdb}
}

func (c *imageTaskCoordinator) ClaimIdempotency(ctx context.Context, scope, fingerprint string, ttl time.Duration) (service.ImageTaskIdempotency, bool, error) {
	key := imageTaskIdempotencyPrefix + scope
	encoded, err := json.Marshal(service.ImageTaskIdempotency{Fingerprint: fingerprint})
	if err != nil {
		return service.ImageTaskIdempotency{}, false, err
	}
	claimed, err := c.rdb.SetNX(ctx, key, encoded, ttl).Result()
	if err != nil {
		return service.ImageTaskIdempotency{}, false, err
	}
	if claimed {
		return service.ImageTaskIdempotency{Fingerprint: fingerprint}, true, nil
	}
	raw, err := c.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		// Expired between SETNX and GET: treat as still being submitted; the
		// client retries after Retry-After and then claims it.
		return service.ImageTaskIdempotency{Fingerprint: fingerprint}, false, nil
	}
	if err != nil {
		return service.ImageTaskIdempotency{}, false, err
	}
	var existing service.ImageTaskIdempotency
	if err := json.Unmarshal(raw, &existing); err != nil {
		return service.ImageTaskIdempotency{}, false, err
	}
	return existing, false, nil
}

func (c *imageTaskCoordinator) BindIdempotency(ctx context.Context, scope string, state service.ImageTaskIdempotency, ttl time.Duration) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, imageTaskIdempotencyPrefix+scope, encoded, ttl).Err()
}

func (c *imageTaskCoordinator) ReleaseIdempotency(ctx context.Context, scope string) error {
	return c.rdb.Del(ctx, imageTaskIdempotencyPrefix+scope).Err()
}

func (c *imageTaskCoordinator) AdmitInFlight(ctx context.Context, apiKeyID int64, taskID string, limit int, now, liveSince time.Time) (bool, error) {
	result, err := admitImageTaskScript.Run(ctx, c.rdb,
		[]string{imageTaskPerKeyInFlightKey(apiKeyID), imageTaskInFlightKey},
		liveSince.UnixMilli(), limit, now.UnixMilli(), taskID, imageTaskInFlightMember(apiKeyID, taskID),
		int64(imageTaskInFlightIndexTTL/time.Second),
	).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *imageTaskCoordinator) TouchInFlight(ctx context.Context, apiKeyID int64, taskID string, now time.Time) error {
	return touchImageTaskScript.Run(ctx, c.rdb,
		[]string{imageTaskPerKeyInFlightKey(apiKeyID), imageTaskInFlightKey},
		now.UnixMilli(), taskID, imageTaskInFlightMember(apiKeyID, taskID),
	).Err()
}

func (c *imageTaskCoordinator) ReleaseInFlight(ctx context.Context, apiKeyID int64, taskID string) error {
	pipe := c.rdb.TxPipeline()
	pipe.ZRem(ctx, imageTaskPerKeyInFlightKey(apiKeyID), taskID)
	pipe.ZRem(ctx, imageTaskInFlightKey, imageTaskInFlightMember(apiKeyID, taskID))
	_, err := pipe.Exec(ctx)
	return err
}

func (c *imageTaskCoordinator) StaleInFlight(ctx context.Context, before time.Time, limit int) ([]service.ImageTaskInFlight, error) {
	members, err := c.rdb.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:     imageTaskInFlightKey,
		Start:   "-inf",
		Stop:    "(" + strconv.FormatInt(before.UnixMilli(), 10),
		ByScore: true,
		Count:   int64(limit),
	}).Result()
	if err != nil {
		return nil, err
	}
	out := make([]service.ImageTaskInFlight, 0, len(members))
	for _, member := range members {
		keyPart, taskID, ok := strings.Cut(member, ":")
		apiKeyID, err := strconv.ParseInt(keyPart, 10, 64)
		if !ok || err != nil || taskID == "" {
			// Unparseable members can never be concluded; drop them.
			_ = c.rdb.ZRem(ctx, imageTaskInFlightKey, member).Err()
			continue
		}
		out = append(out, service.ImageTaskInFlight{TaskID: taskID, APIKeyID: apiKeyID})
	}
	return out, nil
}

func (c *imageTaskCoordinator) TryLock(ctx context.Context, name string, ttl time.Duration) (bool, error) {
	return c.rdb.SetNX(ctx, imageTaskLockPrefix+name, "1", ttl).Result()
}

func (c *imageTaskCoordinator) AllowPoll(ctx context.Context, apiKeyID int64, perSecond int, now time.Time) (bool, error) {
	if perSecond <= 0 {
		return true, nil
	}
	key := imageTaskPollPrefix + strconv.FormatInt(apiKeyID, 10) + ":" + strconv.FormatInt(now.Unix(), 10)
	pipe := c.rdb.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, 2*time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		return true, err
	}
	return incr.Val() <= int64(perSecond), nil
}

func imageTaskPerKeyInFlightKey(apiKeyID int64) string {
	return imageTaskInFlightKeyPrefix + strconv.FormatInt(apiKeyID, 10)
}

func imageTaskInFlightMember(apiKeyID int64, taskID string) string {
	return strconv.FormatInt(apiKeyID, 10) + ":" + taskID
}
