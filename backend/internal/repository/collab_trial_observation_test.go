//go:build unit

package repository

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type collabTrialRedisHook struct{ operations, fullWrites int }

func (h *collabTrialRedisHook) DialHook(redis.DialHook) redis.DialHook {
	return func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("network disabled by probe")
	}
}
func (h *collabTrialRedisHook) ProcessPipelineHook(redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(context.Context, []redis.Cmder) error { return errors.New("unexpected pipeline") }
}
func (h *collabTrialRedisHook) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error {
		args := cmd.Args()
		if cmd.Name() != "evalsha" {
			return fmt.Errorf("unexpected command %s", cmd.Name())
		}
		c, ok := cmd.(*redis.Cmd)
		if !ok {
			return errors.New("wrong command type")
		}
		switch args[1] {
		case schedTrialLeaseScript.Hash():
			h.operations++
			operation := args[5]
			if operation == "room" {
				c.SetVal(int64(0))
			} else if operation == "acquire" {
				c.SetVal("")
			} else {
				return fmt.Errorf("unexpected operation %v", operation)
			}
		case schedTrialRecordFullScript.Hash():
			h.fullWrites++
			c.SetVal(int64(1))
		default:
			return errors.New("unexpected script")
		}
		return nil
	}
}
func TestCollabOptionalTrialFullRedisWriteTrace(t *testing.T) {
	for _, operation := range []string{"room", "acquire"} {
		for _, optional := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/optional_%t", operation, optional), func(t *testing.T) {
				rdb := redis.NewClient(&redis.Options{Addr: "network-disabled.invalid:0", MaxRetries: -1})
				defer rdb.Close()
				hook := &collabTrialRedisHook{}
				rdb.AddHook(hook)
				cache := &schedTrialLeaseCache{rdb: rdb, since: func(time.Time) time.Duration { return 0 }}
				ctx := resilience.WithCollabAttempt(t.Context(), resilience.CollabAttempt{Optional: optional})
				var ok bool
				var err error
				if operation == "room" {
					ok, err = cache.Room(ctx, 1, "m", 7, "r:probe", time.Now())
				} else {
					ok, _, err = cache.Acquire(ctx, 1, "m", 7, "r:probe", time.Now())
				}
				require.NoError(t, err)
				require.False(t, ok)
				require.Equal(t, 1, hook.operations)
				t.Logf("optional=%t operation=%s persistent_full_lua_calls=%d", optional, operation, hook.fullWrites)
				want := 1
				if optional {
					want = 0
				}
				assert.Equal(t, want, hook.fullWrites)
			})
		}
	}
}
