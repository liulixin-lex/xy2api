package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const upstreamBalancePrefix = "upstream_balance:"

func ubKey(id int64) string              { return upstreamBalancePrefix + strconv.FormatInt(id, 10) }
func ubStateKey(id int64) string         { return upstreamBalancePrefix + "state:" + strconv.FormatInt(id, 10) }
func ubKindKey(id int64) string          { return upstreamBalancePrefix + "kind:" + strconv.FormatInt(id, 10) }
func ubSiteKey(origin string) string     { return upstreamBalancePrefix + "site:" + origin }
func ubCTKey(origin string) string       { return upstreamBalancePrefix + "ct:" + origin }
func ubCooldownKey(origin string) string { return upstreamBalancePrefix + "cooldown:" + origin }

type upstreamBalanceStore struct{ rdb *redis.Client }

func NewUpstreamBalanceStore(rdb *redis.Client) service.UpstreamBalanceStore {
	return &upstreamBalanceStore{rdb: rdb}
}
func (s *upstreamBalanceStore) Load(ctx context.Context, ids []int64) (*service.UpstreamBalanceEntries, error) {
	out := &service.UpstreamBalanceEntries{Records: map[int64]*service.UpstreamBalanceRecord{}, States: map[int64]*service.UpstreamBalanceState{}, Kinds: map[int64]*service.UpstreamBalanceKindRecord{}}
	if len(ids) == 0 {
		return out, nil
	}
	for start := 0; start < len(ids); start += 333 {
		end := start + 333
		if end > len(ids) {
			end = len(ids)
		}
		keys := make([]string, 0, (end-start)*3)
		for _, id := range ids[start:end] {
			keys = append(keys, ubKey(id), ubStateKey(id), ubKindKey(id))
		}
		vals, err := s.rdb.MGet(ctx, keys...).Result()
		if err != nil {
			return nil, err
		}
		for i, id := range ids[start:end] {
			var rec service.UpstreamBalanceRecord
			if decodeRedis(vals[i*3], &rec) {
				out.Records[id] = &rec
			}
			var st service.UpstreamBalanceState
			if decodeRedis(vals[i*3+1], &st) {
				out.States[id] = &st
			}
			var kind service.UpstreamBalanceKindRecord
			if decodeRedis(vals[i*3+2], &kind) {
				out.Kinds[id] = &kind
			}
		}
	}
	return out, nil
}
func decodeRedis(v any, dst any) bool {
	if v == nil {
		return false
	}
	var b []byte
	switch x := v.(type) {
	case string:
		b = []byte(x)
	case []byte:
		b = x
	default:
		return false
	}
	return json.Unmarshal(b, dst) == nil
}
func (s *upstreamBalanceStore) SaveSuccess(ctx context.Context, id int64, rec *service.UpstreamBalanceRecord, st *service.UpstreamBalanceState) error {
	if rec == nil || st == nil {
		return errors.New("upstream balance record is nil")
	}
	rb, e := json.Marshal(rec)
	if e != nil {
		return e
	}
	sb, e := json.Marshal(st)
	if e != nil {
		return e
	}
	_, e = s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Set(ctx, ubKey(id), rb, 24*time.Hour)
		p.Set(ctx, ubStateKey(id), sb, 48*time.Hour)
		return nil
	})
	return e
}
func (s *upstreamBalanceStore) SaveAttempt(ctx context.Context, id int64, st *service.UpstreamBalanceState) error {
	if st == nil {
		return errors.New("upstream balance state is nil")
	}
	b, e := json.Marshal(st)
	if e != nil {
		return e
	}
	return s.rdb.Set(ctx, ubStateKey(id), b, 48*time.Hour).Err()
}
func (s *upstreamBalanceStore) SaveKind(ctx context.Context, id int64, k *service.UpstreamBalanceKindRecord, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("invalid upstream balance ttl")
	}
	b, e := json.Marshal(k)
	if e != nil {
		return e
	}
	return s.rdb.Set(ctx, ubKindKey(id), b, ttl).Err()
}
func (s *upstreamBalanceStore) DeleteKind(ctx context.Context, id int64) error {
	return s.rdb.Del(ctx, ubKindKey(id)).Err()
}
func (s *upstreamBalanceStore) GetSite(ctx context.Context, o string) (*service.UpstreamBalanceSite, error) {
	var v service.UpstreamBalanceSite
	b, err := s.rdb.Get(ctx, ubSiteKey(o)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return &v, nil
}
func (s *upstreamBalanceStore) SaveSite(ctx context.Context, o string, v *service.UpstreamBalanceSite, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("invalid upstream balance ttl")
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return s.rdb.Set(ctx, ubSiteKey(o), b, ttl).Err()
}
func (s *upstreamBalanceStore) TryLockOrigin(ctx context.Context, o, owner string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		return false, errors.New("invalid upstream balance ttl")
	}
	return s.rdb.SetNX(ctx, ubCTKey(o), owner, ttl).Result()
}
func (s *upstreamBalanceStore) GetCooldown(ctx context.Context, origin string) (*time.Time, error) {
	v, err := s.rdb.Get(ctx, ubCooldownKey(origin)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
func (s *upstreamBalanceStore) SaveCooldown(ctx context.Context, origin string, notBefore time.Time) error {
	ttl := time.Until(notBefore)
	if ttl <= 0 {
		return nil
	}
	key := ubCooldownKey(origin)
	value := notBefore.UTC().Format(time.RFC3339Nano)
	ms := ttl.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	return s.rdb.Eval(ctx, `local ttl = redis.call('PTTL', KEYS[1]); if ttl >= tonumber(ARGV[2]) then return 0 end; redis.call('PSETEX', KEYS[1], ARGV[2], ARGV[1]); return 1`, []string{key}, value, ms).Err()
}
