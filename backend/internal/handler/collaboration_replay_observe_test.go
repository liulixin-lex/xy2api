//go:build unit

package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
)

// Observe private learning state without modifying it or exposing test hooks in
// production. Calls happen at synchronous send/completion boundaries. Nothing
// from the private state is persisted except keyed digests and fixed enums.
func replayLearning(gateway *service.OpenAIGatewayService) []string {
	out := []string{}
	store := reflect.ValueOf(gateway).Elem().FieldByName("openaiWSStateStore")
	if store.IsNil() {
		return out
	}
	store = store.Elem().Elem()
	for _, name := range []string{"sessionInvalidEncrypted", "sessionBadEncrypted"} {
		entries := store.FieldByName(name)
		for _, key := range entries.MapKeys() {
			value := entries.MapIndex(key)
			scope := ""
			if key.Kind() == reflect.String {
				scope = key.String()
			} else {
				scope = fmt.Sprintf("%d:%s:%d", key.FieldByName("groupID").Int(), key.FieldByName("session").String(), key.FieldByName("accountID").Int())
			}
			field := "digests"
			if name == "sessionBadEncrypted" {
				field = "slots"
			}
			for _, learned := range value.FieldByName(field).MapKeys() {
				payload := learned.String()
				if learned.Kind() == reflect.Struct {
					payload = learned.FieldByName("kind").String() + ":" + learned.FieldByName("ref").String() + ":" + learned.FieldByName("digest").String()
				}
				out = append(out, replayDigest([]byte(name+":"+scope+":"+payload)))
			}
		}
	}
	sort.Strings(out)
	return out
}

type replayWriter struct {
	*httptest.ResponseRecorder
	last                             time.Time
	maxIdle                          time.Duration
	start                            time.Time
	headerAt, terminalAt, acceptedAt int64
	beforeTerminal                   bool
	nonterminal                      bool
	upstream                         *replayUpstream
	deliveredSend                    int
}

func (w *replayWriter) WriteHeader(status int) {
	if w.headerAt < 0 {
		w.headerAt = time.Since(w.start).Milliseconds()
	}
	w.ResponseRecorder.WriteHeader(status)
}
func (w *replayWriter) observe(raw string) {
	terminal := strings.Contains(raw, `"error":`) || strings.Contains(raw, "response.failed") || strings.Contains(raw, "response.completed") || strings.Contains(raw, `"status":"completed"`)
	accepted := strings.Contains(raw, "response.output_text.delta") || strings.Contains(raw, "response.output_item.done") || strings.Contains(raw, "response.completed") || strings.Contains(raw, `"status":"completed"`)
	if accepted && w.deliveredSend < 0 && w.upstream != nil {
		w.deliveredSend = len(w.upstream.sends) - 1
	}
	if accepted && w.acceptedAt < 0 {
		w.acceptedAt = time.Since(w.start).Milliseconds()
	}
	if terminal && w.terminalAt < 0 {
		w.terminalAt = time.Since(w.start).Milliseconds()
		w.beforeTerminal = w.nonterminal || w.headerAt >= 0 && w.headerAt < w.terminalAt
	}
	if !terminal {
		w.nonterminal = true
	}
}

func (w *replayWriter) note() {
	now := time.Now()
	if gap := now.Sub(w.last); gap > w.maxIdle {
		w.maxIdle = gap
	}
	w.last = now
}
func (w *replayWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.observe(string(p))
		w.note()
	}
	return w.ResponseRecorder.Write(p)
}
func (w *replayWriter) WriteString(s string) (int, error) {
	if s != "" {
		w.observe(s)
		w.note()
	}
	return w.ResponseRecorder.WriteString(s)
}
func (w *replayWriter) Flush() {
	if w.terminalAt < 0 {
		w.nonterminal = true
	}
	w.note()
	w.ResponseRecorder.Flush()
}

type replaySlots struct {
	concurrencyCacheMock
	mu            sync.Mutex
	base          int64
	firstWaiters  bool
	secondWaiters bool
	secondFull    bool
	userWaiters   bool
	active        map[int64]int
}

func (s *replaySlots) AcquireUserSlot(context.Context, int64, int, string) (bool, error) {
	return true, nil
}
func (s *replaySlots) GetUserConcurrency(context.Context, int64) (int, error) {
	if s.userWaiters {
		return 1, nil
	}
	return 0, nil
}
func (s *replaySlots) GetUsersLoadBatch(ctx context.Context, users []service.UserWithConcurrency) (map[int64]*service.UserLoadInfo, error) {
	if collabUserLoadFault != nil {
		if err := collabUserLoadFault(ctx); err != nil {
			return nil, err
		}
	}
	out := map[int64]*service.UserLoadInfo{}
	for _, user := range users {
		n, waiters := 0, 0
		if s.userWaiters {
			n, waiters = 1, 1
		}
		out[user.ID] = &service.UserLoadInfo{UserID: user.ID, CurrentConcurrency: n, WaitingCount: waiters, LoadRate: n * 100}
	}
	return out, nil
}

func replayTerminalCounters(runtime *service.ResilienceService) []string {
	out := []string{}
	entries := reflect.ValueOf(runtime).Elem().FieldByName("terminalRejections").FieldByName("entries")
	for _, key := range entries.MapKeys() {
		entry := entries.MapIndex(key).Elem().FieldByName("Value").Elem().Elem()
		var digest []byte
		for i := 0; i < entry.FieldByName("digest").Len(); i++ {
			digest = append(digest, byte(entry.FieldByName("digest").Index(i).Uint()))
		}
		payload := fmt.Sprintf("%d:%s:%d:%x:%d:%t", key.FieldByName("group").Int(), key.FieldByName("session").String(), key.FieldByName("account").Int(), digest, entry.FieldByName("streak").Int(), entry.FieldByName("crossHost").Bool())
		out = append(out, replayDigest([]byte(payload)))
	}
	sort.Strings(out)
	return out
}

func (s *replaySlots) AcquireAccountSlot(_ context.Context, id int64, limit int, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == s.base+2 && s.secondFull || s.active[id] >= limit {
		return false, nil
	}
	s.active[id]++
	return true, nil
}
func (s *replaySlots) ReleaseAccountSlot(_ context.Context, id int64, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[id]--
	return nil
}
func (s *replaySlots) GetAccountConcurrency(_ context.Context, id int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == s.base+2 && s.secondFull {
		return 2, nil
	}
	return s.active[id], nil
}
func (s *replaySlots) GetAccountConcurrencyBatch(ctx context.Context, ids []int64) (map[int64]int, error) {
	out := map[int64]int{}
	for _, id := range ids {
		n, err := s.GetAccountConcurrency(ctx, id)
		if err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, nil
}
func (s *replaySlots) GetAccountWaitingCount(_ context.Context, id int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == s.base+1 && s.firstWaiters || id == s.base+2 && s.secondWaiters {
		return 1, nil
	}
	return 0, nil
}
func (s *replaySlots) GetAccountsLoadBatch(ctx context.Context, accounts []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	out := map[int64]*service.AccountLoadInfo{}
	for _, account := range accounts {
		n, err := s.GetAccountConcurrency(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		waiters, err := s.GetAccountWaitingCount(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		out[account.ID] = &service.AccountLoadInfo{AccountID: account.ID, CurrentConcurrency: n, WaitingCount: waiters, LoadRate: 50 * n}
	}
	return out, nil
}

// The new shared record API does not exist on the baseline. Integration supplies
// this adapter; slow/failing stores are deliberately not wired to sticky state.
var configureReplayRecords func(*testing.T, *service.OpenAIGatewayService, *replayRecordStore)
var configureReplayHistory func(*testing.T, *service.OpenAIGatewayService, replayCase)
var configureReplayHandler func(*OpenAIGatewayHandler, *openAIWSUsageHandlerUsageLogRepoStub)

// replayGroupLookup completes the repository stub. The goldens have it off.
var replayGroupLookup bool

type replayRecordStore struct {
	mu    sync.Mutex
	mode  string
	calls int
}

func (s *replayRecordStore) Operation(ctx context.Context) error {
	s.mu.Lock()
	s.calls++
	mode := s.mode
	s.mu.Unlock()
	switch mode {
	case "slow":
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(61 * time.Second):
			return nil
		}
	case "failure":
		return errors.New("synthetic record failure")
	case "panic":
		panic("synthetic record panic")
	default:
		return nil
	}
}
func (s *replayRecordStore) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }
