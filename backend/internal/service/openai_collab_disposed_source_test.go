//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"

	"bytes"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"
)

func TestCollabDisposedSourceEndpoint(t *testing.T) {
	for _, thread := range []string{"child", "main"} {
		for _, stream := range []bool{false, true} {
			for _, named := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream_%t/named_%t", thread, stream, named), func(t *testing.T) {
					raw, err := sjson.Set(collabBatchBody, "stream", stream)
					require.NoError(t, err)
					p := ParseCollabBodyWithContext([]byte(raw), CollabBodyContext{Thread: thread})
					disposed, changed := p.Derive(p.Original, resilience.CollabActionReplaceAll, "", false)
					require.True(t, changed)
					if stream {
						disposed = bytes.ReplaceAll(disposed, []byte(collabUnreadTask), []byte("[encrypted content omitted]"))
					}
					payload := `{"error":{"code":"invalid_encrypted_content"}}`
					if named {
						payload = `{"error":{"code":"invalid_encrypted_content","item_id":"amsg_task"}}`
					}
					r := collabObserveDerivedRejection(p, disposed, payload)
					r.AccountID = 1
					progress := &CollabLadderProgress{sourceBody: disposed}
					progress.RememberRejection(r)
					l := CollabLadder{Body: p, First: 1, Current: 1, Progress: progress, Evidence: r.Evidence(p.Shape), MaxAttempts: 1, Select: collabBatchSelect,
						Send: func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
							t.Fatal("fully disposed rejection must not be sent again")
							return resilience.CollabEvidence{}, false
						}}
					require.False(t, l.Run(context.Background()))
					require.Equal(t, CollabLadderFinalDisposal, l.StopReason)
					require.True(t, p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding)
				})
			}
		}
	}
}
