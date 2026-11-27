package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const compensationPrefix = "xy:resil:compensation:"

func compensationRedisKeys(key string) (string, string) {
	base := compensationPrefix + "{" + key + "}:"
	return base + "reference", base + "action"
}

var compensationReferenceScript = redis.NewScript(`
 local old=redis.call('GET',KEYS[1])
 if old and tonumber(cjson.decode(old).at_unix_ms)>=tonumber(ARGV[2]) then return 0 end
 redis.call('SET',KEYS[1],ARGV[1],'PX',ARGV[3])
 return 1
`)
var compensationFreezeScript = redis.NewScript(`
 if redis.call('EXISTS',KEYS[2])==1 then return 0 end
 local action=cjson.decode(ARGV[1])
 local ref=redis.call('GET',KEYS[1])
 if ref then action.reference=cjson.decode(ref) end
 redis.call('SET',KEYS[2],cjson.encode(action),'PX',ARGV[2])
 return 1
`)
var compensationTakeScript = redis.NewScript(`
 local claimed=redis.call('GET',KEYS[2])
 if claimed then return claimed end
 local action=redis.call('GET',KEYS[1])
 if action then
  local a=cjson.decode(action)
  if a.kind=='request_switch' and a.request_id~=ARGV[1] then return nil end
  -- The triggering bill may be emitted after a post-output owner release.
  -- It is not the next bill and cannot consume the next owner's reference.
  if a.request_id==ARGV[1] and a.kind~='request_switch' and a.from==tonumber(ARGV[2]) then return nil end
  redis.call('SET',KEYS[2],action,'PX',ARGV[3])
  redis.call('DEL',KEYS[1])
 end
 return action
`)

func (s *resilienceStore) SetCompensationReference(ctx context.Context, key string, ref service.OpenAICompensationReference) error {
	if key == "" {
		return nil
	}
	raw, err := json.Marshal(struct {
		service.OpenAICompensationReference
		AtUnixMs int64 `json:"at_unix_ms"`
	}{ref, ref.At.UnixMilli()})
	if err != nil {
		return err
	}
	reference, _ := compensationRedisKeys(key)
	return compensationReferenceScript.Run(ctx, s.rdb, []string{reference}, string(raw), ref.At.UnixMilli(), int64(10*time.Minute/time.Millisecond)).Err()
}
func (s *resilienceStore) FreezeCompensationAction(ctx context.Context, key string, action service.OpenAICompensationAction) error {
	if key == "" {
		return nil
	}
	raw, err := json.Marshal(action)
	if err != nil {
		return err
	}
	reference, pending := compensationRedisKeys(key)
	return compensationFreezeScript.Run(ctx, s.rdb, []string{reference, pending}, string(raw), int64(time.Hour/time.Millisecond)).Err()
}
func (s *resilienceStore) TakeCompensationAction(ctx context.Context, key, requestID string, accountID int64) (*service.OpenAICompensationAction, error) {
	if key == "" || requestID == "" {
		return nil, nil
	}
	_, pending := compensationRedisKeys(key)
	claimID := sha256.Sum256([]byte(requestID))
	claimed := pending + ":claimed:" + hex.EncodeToString(claimID[:])
	raw, err := compensationTakeScript.Run(ctx, s.rdb, []string{pending, claimed}, requestID, accountID, int64(time.Hour/time.Millisecond)).Text()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var action service.OpenAICompensationAction
	if err = json.Unmarshal([]byte(raw), &action); err != nil {
		return nil, err
	}
	return &action, nil
}
