package repository

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	shareSchedulingChannel = "xy2:share:policy"
	// Counters are kept per minute for short windows and per hour beyond:
	// per-account fields grow with the group, and a day of minute buckets
	// would hold 1440 copies of them. Hourly buckets live as long as their
	// kind needs (service.ShareCacheCounterRetention and
	// service.ShareShareCounterRetention).
	shareSchedulingMinuteTTL = 2*time.Hour + 10*time.Minute
	// shareSchedulingMinuteSpan is the longest window read minute by minute.
	// Longer windows read whole hours from the one nearest the window start,
	// or from the next one for an exact read, plus the minutes of the
	// current hour.
	shareSchedulingMinuteSpan = 2 * time.Hour
	// shareSchedulingMaxWindow bounds one report read to a day.
	shareSchedulingMaxWindow = 24 * time.Hour
)

// shareHourCounterTTL is how long an hourly bucket of a counter kind lives.
func shareHourCounterTTL(kind string) time.Duration {
	if kind == "cache" {
		return service.ShareCacheCounterRetention
	}
	return service.ShareShareCounterRetention
}

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

// shareSwitchKey holds the time of the group's last scheduler switch in
// Unix milliseconds.
func shareSwitchKey(groupID int64) string {
	return fmt.Sprintf("xy2:share:switched:%d", groupID)
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

func (s *shareSchedulingStore) WriteStats(ctx context.Context, batch service.ShareSchedulingStatsBatch) error {
	pipe := s.rdb.Pipeline()
	expire := map[string]time.Duration{}
	for counter, n := range batch.Counters {
		minuteKey := shareCounterKey(counter.Kind, counter.GroupID, counter.Minute)
		hourKey := shareHourCounterKey(counter.Kind, counter.GroupID, counter.Minute/60)
		pipe.HIncrBy(ctx, minuteKey, counter.Field, n)
		pipe.HIncrBy(ctx, hourKey, counter.Field, n)
		expire[minuteKey] = shareSchedulingMinuteTTL
		expire[hourKey] = shareHourCounterTTL(counter.Kind)
	}
	for key, ttl := range expire {
		pipe.Expire(ctx, key, ttl)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (s *shareSchedulingStore) ReadCounters(ctx context.Context, kind string, groupID int64, from, to time.Time, exact bool) (map[string]int64, error) {
	if to.Before(from) {
		return map[string]int64{}, nil
	}
	if to.Sub(from) > shareSchedulingMaxWindow {
		from = to.Add(-shareSchedulingMaxWindow)
	}
	first, last := from.Unix()/60, to.Unix()/60
	pipe := s.rdb.Pipeline()
	var cmds []*redis.MapStringStringCmd
	minutes := func(from, to int64) {
		for minute := from; minute < to; minute++ {
			cmds = append(cmds, pipe.HGetAll(ctx, shareCounterKey(kind, groupID, minute)))
		}
	}
	if to.Sub(from) > shareSchedulingMinuteSpan {
		current := to.Unix() / 3600
		start := (from.Unix() + 1800) / 3600
		if exact {
			// The hour bucket at from holds earlier data: read that
			// partial hour minute by minute while all its buckets are
			// kept, or leave it out.
			start = (from.Unix() + 3599) / 3600
			if to.Before(from.Truncate(time.Minute).Add(shareSchedulingMinuteTTL)) {
				minutes(first, start*60)
			}
		}
		for hour := start; hour < current; hour++ {
			cmds = append(cmds, pipe.HGetAll(ctx, shareHourCounterKey(kind, groupID, hour)))
		}
		first = current * 60
	}
	minutes(first, last+1)
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	return sumShareCounters(cmds), nil
}

// ReadHourCounters sums the hourly buckets [fromHour, toHour), at most a
// day of them.
func (s *shareSchedulingStore) ReadHourCounters(ctx context.Context, kind string, groupID int64, fromHour, toHour int64) (map[string]int64, error) {
	fromHour = max(fromHour, toHour-int64(shareSchedulingMaxWindow/time.Hour))
	pipe := s.rdb.Pipeline()
	var cmds []*redis.MapStringStringCmd
	for hour := fromHour; hour < toHour; hour++ {
		cmds = append(cmds, pipe.HGetAll(ctx, shareHourCounterKey(kind, groupID, hour)))
	}
	if len(cmds) == 0 {
		return map[string]int64{}, nil
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	return sumShareCounters(cmds), nil
}

func sumShareCounters(cmds []*redis.MapStringStringCmd) map[string]int64 {
	out := map[string]int64{}
	for _, cmd := range cmds {
		for field, raw := range cmd.Val() {
			if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
				out[field] += n
			}
		}
	}
	return out
}

func (s *shareSchedulingStore) RecordSwitch(ctx context.Context, groupID int64, at time.Time) error {
	return s.rdb.Set(ctx, shareSwitchKey(groupID), strconv.FormatInt(at.UnixMilli(), 10), service.ShareSwitchAnchorTTL).Err()
}

func (s *shareSchedulingStore) SwitchedAt(ctx context.Context, groupID int64) (time.Time, error) {
	raw, err := s.rdb.Get(ctx, shareSwitchKey(groupID)).Result()
	if errors.Is(err, redis.Nil) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}, nil
	}
	return time.UnixMilli(ms), nil
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
