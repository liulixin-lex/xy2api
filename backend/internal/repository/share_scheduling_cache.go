package repository

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	shareSchedulingChannel = "xy2:share:policy"
	// Counters are kept per minute for short windows and per hour for the
	// day: per-account fields grow with the group, and a day of minute
	// buckets would hold 1440 copies of them.
	shareSchedulingMinuteTTL   = 2*time.Hour + 10*time.Minute
	shareSchedulingCounterTTL  = 26 * time.Hour
	shareSchedulingDecisionTTL = 24 * time.Hour
	shareSchedulingDecisionMax = 500
	// shareSchedulingMinuteSpan is the longest window read minute by minute.
	// Longer windows read whole hours from the one nearest the window start,
	// plus the minutes of the current hour.
	shareSchedulingMinuteSpan = 2 * time.Hour
	// shareSchedulingMaxWindow bounds one report read to a day.
	shareSchedulingMaxWindow = 24 * time.Hour
	shareSchedulingScanCount = 500
)

// shareDeleteBindingIfOwner deletes a session binding only while it still
// points at the account the rebalance read, so a session that was rebound in
// between keeps its new binding. It returns 1 when it deleted.
const shareDeleteBindingIfOwner = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

type shareSchedulingStore struct {
	rdb *redis.Client
}

// NewShareSchedulingStore is the Redis side of share scheduling.
func NewShareSchedulingStore(rdb *redis.Client) service.ShareSchedulingStore {
	return &shareSchedulingStore{rdb: rdb}
}

func shareCounterKey(kind string, groupID, minute int64) string {
	return fmt.Sprintf("xy2:%s:attr:%d:%d", kind, groupID, minute)
}

func shareHourCounterKey(kind string, groupID, hour int64) string {
	return fmt.Sprintf("xy2:%s:attrh:%d:%d", kind, groupID, hour)
}

func shareDecisionKey(groupID int64) string {
	return fmt.Sprintf("xy2:share:dec:%d", groupID)
}

func shareCacheSessionKey(groupID int64, fingerprint string) string {
	return fmt.Sprintf("xy2:cache:sess:%d:%s", groupID, fingerprint)
}

func (s *shareSchedulingStore) PublishChange(ctx context.Context, groupID int64) error {
	return s.rdb.Publish(ctx, shareSchedulingChannel, strconv.FormatInt(groupID, 10)).Err()
}

func (s *shareSchedulingStore) SubscribeChanges(ctx context.Context, handler func(groupID int64)) {
	go func() {
		pubsub := s.rdb.Subscribe(ctx, shareSchedulingChannel)
		defer func() {
			if err := pubsub.Close(); err != nil {
				slog.Warn("share_scheduling.subscriber_close_failed", "error", err)
			}
		}()
		messages := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case message, ok := <-messages:
				if !ok {
					slog.Warn("share_scheduling.subscriber_stopped", "reason", "channel_closed")
					return
				}
				if message == nil {
					continue
				}
				groupID, err := strconv.ParseInt(message.Payload, 10, 64)
				if err != nil {
					continue
				}
				handler(groupID)
			}
		}
	}()
}

// A bucket and its hourly copy commit together. Batch markers make retries
// safe after a partial pipeline failure or a lost ACK: neither counters nor
// decision samples can be counted twice.
const shareWriteCounterBucket = `
if redis.call('EXISTS', KEYS[3]) == 1 then return 0 end
for i=1,2 do
  local typ = redis.call('TYPE', KEYS[i]).ok
  if typ ~= 'none' and typ ~= 'hash' then return redis.error_reply('invalid stats bucket type') end
  for j=3,#ARGV,2 do
    local old = redis.call('HGET', KEYS[i], ARGV[j])
    local n = tonumber(ARGV[j+1])
    if not string.match(ARGV[j+1], '^%-?%d+$') or not n or (old and not string.match(old, '^%-?%d+$')) or math.abs(tonumber(old or '0')+n) > 9000000000000000 then
      return redis.error_reply('invalid stats counter')
    end
  end
end
for j=3,#ARGV,2 do
  redis.call('HINCRBY', KEYS[1], ARGV[j], ARGV[j+1])
  redis.call('HINCRBY', KEYS[2], ARGV[j], ARGV[j+1])
end
redis.call('EXPIRE', KEYS[1], ARGV[1])
redis.call('EXPIRE', KEYS[2], ARGV[2])
redis.call('SET', KEYS[3], '1', 'EX', ARGV[2])
return 1
`
const shareWriteDecisionBatch = `
if redis.call('EXISTS', KEYS[2]) == 1 then return 0 end
local typ = redis.call('TYPE', KEYS[1]).ok
if typ ~= 'none' and typ ~= 'list' then return redis.error_reply('invalid decision list type') end
for i=3,#ARGV do redis.call('LPUSH', KEYS[1], ARGV[i]) end
redis.call('LTRIM', KEYS[1], 0, tonumber(ARGV[2])-1)
redis.call('EXPIRE', KEYS[1], ARGV[1])
redis.call('SET', KEYS[2], '1', 'EX', ARGV[1])
return 1
`

func (s *shareSchedulingStore) WriteStats(ctx context.Context, batch service.ShareSchedulingStatsBatch) error {
	id := batch.ID
	if id == "" {
		id = rand.Text()
	}
	type bucket struct {
		kind          string
		group, minute int64
	}
	buckets := map[bucket][]any{}
	for counter, n := range batch.Counters {
		key := bucket{counter.Kind, counter.GroupID, counter.Minute}
		buckets[key] = append(buckets[key], counter.Field, n)
	}
	pipe := s.rdb.Pipeline()
	for b, fields := range buckets {
		minute := shareCounterKey(b.kind, b.group, b.minute)
		hour := shareHourCounterKey(b.kind, b.group, b.minute/60)
		marker := fmt.Sprintf("xy2:stats:batch:%s:%s:%d:%d", id, b.kind, b.group, b.minute)
		args := append([]any{int64(shareSchedulingMinuteTTL / time.Second), int64(shareSchedulingCounterTTL / time.Second)}, fields...)
		pipe.Eval(ctx, shareWriteCounterBucket, []string{minute, hour, marker}, args...)
	}
	for groupID, samples := range batch.Decisions {
		if len(samples) == 0 {
			continue
		}
		key := shareDecisionKey(groupID)
		args := []any{int64(shareSchedulingDecisionTTL / time.Second), shareSchedulingDecisionMax}
		for _, sample := range samples {
			args = append(args, sample)
		}
		pipe.Eval(ctx, shareWriteDecisionBatch, []string{key, key + ":batch:" + id}, args...)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (s *shareSchedulingStore) ReadCounters(ctx context.Context, kind string, groupID int64, from, to time.Time) (map[string]int64, error) {
	if to.Before(from) {
		return map[string]int64{}, nil
	}
	if to.Sub(from) > shareSchedulingMaxWindow {
		from = to.Add(-shareSchedulingMaxWindow)
	}
	first, last := from.Unix()/60, to.Unix()/60
	pipe := s.rdb.Pipeline()
	var cmds []*redis.MapStringStringCmd
	if to.Sub(from) > shareSchedulingMinuteSpan {
		current := to.Unix() / 3600
		for hour := (from.Unix() + 1800) / 3600; hour < current; hour++ {
			cmds = append(cmds, pipe.HGetAll(ctx, shareHourCounterKey(kind, groupID, hour)))
		}
		first = current * 60
	}
	for minute := first; minute <= last; minute++ {
		cmds = append(cmds, pipe.HGetAll(ctx, shareCounterKey(kind, groupID, minute)))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	out := map[string]int64{}
	for _, cmd := range cmds {
		for field, raw := range cmd.Val() {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return nil, errors.New("invalid share scheduling counter value")
			}
			out[field] += n
		}
	}
	return out, nil
}

func (s *shareSchedulingStore) RecentDecisions(ctx context.Context, groupID int64, limit int) ([][]byte, error) {
	if limit <= 0 || limit > shareSchedulingDecisionMax {
		limit = shareSchedulingDecisionMax
	}
	values, err := s.rdb.LRange(ctx, shareDecisionKey(groupID), 0, int64(limit-1)).Result()
	if err != nil {
		return nil, err
	}
	out := make([][]byte, len(values))
	for i, v := range values {
		out[i] = []byte(v)
	}
	return out, nil
}

func (s *shareSchedulingStore) SwapCacheSession(ctx context.Context, groupID int64, fingerprint string, value []byte, ttl time.Duration) ([]byte, error) {
	previous, err := s.rdb.SetArgs(ctx, shareCacheSessionKey(groupID, fingerprint), value, redis.SetArgs{TTL: ttl, Get: true}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(previous), nil
}

// ClearBindings scans the group's session bindings. Protocol continuation
// and ownership keys stored under the same prefix are never touched, and a
// binding is only deleted while it still holds the account that was read.
func (s *shareSchedulingStore) ClearBindings(ctx context.Context, groupID int64, clear func(service.ShareSchedulingBinding) bool) (int, error) {
	prefix := buildSessionKey(groupID, "")
	var cursor uint64
	deleted := 0
	for {
		keys, next, err := s.rdb.Scan(ctx, cursor, prefix+"*", shareSchedulingScanCount).Result()
		if err != nil {
			return deleted, err
		}
		candidates := make([]string, 0, len(keys))
		for _, key := range keys {
			if service.IsShareRebalanceableBinding(strings.TrimPrefix(key, prefix)) {
				candidates = append(candidates, key)
			}
		}
		if len(candidates) > 0 {
			pipe := s.rdb.Pipeline()
			gets := make([]*redis.StringCmd, len(candidates))
			ttls := make([]*redis.DurationCmd, len(candidates))
			for i, key := range candidates {
				gets[i] = pipe.Get(ctx, key)
				ttls[i] = pipe.PTTL(ctx, key)
			}
			if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
				return deleted, err
			}
			var removes []*redis.Cmd
			removePipe := s.rdb.Pipeline()
			for i, key := range candidates {
				accountID, err := gets[i].Int64()
				if err != nil {
					continue
				}
				if clear(service.ShareSchedulingBinding{AccountID: accountID, Remaining: ttls[i].Val()}) {
					removes = append(removes, removePipe.Eval(ctx, shareDeleteBindingIfOwner, []string{key}, strconv.FormatInt(accountID, 10)))
				}
			}
			if len(removes) > 0 {
				_, err := removePipe.Exec(ctx)
				for _, cmd := range removes {
					if n, cmdErr := cmd.Int64(); cmdErr == nil {
						deleted += int(n)
					}
				}
				if err != nil {
					return deleted, err
				}
			}
		}
		cursor = next
		if cursor == 0 {
			return deleted, nil
		}
	}
}
