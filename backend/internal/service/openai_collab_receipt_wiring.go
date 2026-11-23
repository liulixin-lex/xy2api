package service

import (
	"context"
	"strconv"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// The legacy rejection cache is ciphertext-scoped and can prestrip a task
// before its first send. A provisional local ending without this request's
// receipt must instead try the exact original delivery first. Later repairs
// retain their existing timing and budgets.
func protectCollabUnreceiptedFirstSend(ctx context.Context, body []byte) (out []byte) {
	out = body
	defer func() {
		if recover() != nil {
			out = body
			resilience.Count("collab_wiring_panic", 0, 0, "receipt")
		}
	}()
	r := openAICollabRequestFrom(ctx)
	if r == nil || !r.settings.CollabLadderOn() || r.state.Sent != 0 || resilience.CollabAttemptFrom(ctx).Optional {
		return
	}
	receipts := r.gateway.ParseCollabNoticeReceipts(r.state.Original)
	now := time.Now()
	for i, item := range r.state.Items {
		pending := false
		effective := false
		for _, record := range item.Dispositions.Terminated {
			if record.Key == item.Key && record.ExpiresAt.After(now) {
				pending = true
				effective = effective || resilience.CollabTerminationEffective(record, item.Key, receipts, now)
			}
		}
		if !pending || effective || i >= len(r.itemPaths) {
			continue
		}
		base := "input." + strconv.Itoa(item.Position.InputIndex)
		if gjson.GetBytes(body, base+".id").String() != gjson.GetBytes(r.state.Original, base+".id").String() {
			continue
		}
		original := gjson.GetBytes(r.state.Original, r.itemPaths[i])
		changed, err := sjson.SetRawBytes(out, r.itemPaths[i], []byte(original.Raw))
		if err != nil {
			return body
		}
		out = changed
	}
	return
}
