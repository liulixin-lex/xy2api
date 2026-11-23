//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCollabLadderSavedErrorStateSpace(t *testing.T) {
	const original = `{"input":[{"type":"agent_message","id":"amsg_task","content":[{"type":"encrypted_content","encrypted_content":"synthetic"}]}]}`
	const success = `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`
	verified := resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeVerified}
	for behaviours := 0; behaviours < 16; behaviours++ {
		for slots := 0; slots < 4; slots++ {
			for _, limit := range []int{1, 2, 4, 0} {
				for _, committed := range []bool{false, true} {
					for _, expired := range []bool{false, true} {
						for _, legacySuccess := range []bool{false, true} {
							recorder := httptest.NewRecorder()
							c, _ := gin.CreateTestContext(recorder)
							c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
							sends, acquired, released := 0, 0, 0
							ctx, cancel := context.WithCancel(c.Request.Context())
							if expired {
								cancel()
							}
							defer cancel()
							withSeam(c, func(c *gin.Context, seam *collabSeam, _ *collabWithheld) {
								ladder := service.CollabLadder{Body: service.ParseCollabBody([]byte(original)), First: 1, Evidence: verified, MaxAttempts: limit}
								ladder.Select = func(_ context.Context, prefer int64, excluded map[int64]bool) (int64, func()) {
									for _, id := range []int64{prefer, 1, 2} {
										if id > 0 && !excluded[id] && slots&(1<<(id-1)) != 0 {
											acquired++
											return id, func() { released++ }
										}
									}
									return 0, nil
								}
								ladder.Send = func(_ context.Context, id int64, _ []byte, action resilience.CollabAction) (resilience.CollabEvidence, bool) {
									seam.resetAttempt(c)
									sends++
									accepted := behaviours&(1<<(id-1)) != 0 || action == resilience.CollabActionReplaceAll && behaviours&(1<<(id+1)) != 0
									if accepted {
										if committed {
											seamTestStream(c, seamTestDelta)
										} else {
											c.Data(http.StatusOK, "application/json", []byte(success))
										}
									} else {
										c.Data(503, "application/json", []byte(`{"error":{"message":"optional refusal"}}`))
									}
									return verified, seam.writer.current() == collabSeamPassing
								}
								ladder.Run(ctx)
							}, func() {
								if legacySuccess {
									c.Data(200, "application/json", []byte(success))
									return
								}
								if committed {
									seamTestStream(c, seamTestPing, seamTestFailed)
								} else {
									seamTestExistingError(c)()
								}
							})
							require.Equal(t, acquired, released)
							if legacySuccess {
								require.Equal(t, success, recorder.Body.String())
								require.Zero(t, sends)
								continue
							}
							if committed {
								require.Contains(t, []string{seamTestPing + seamTestFailed, seamTestPing + seamTestDelta}, recorder.Body.String())
							} else if recorder.Code >= 400 {
								require.Equal(t, 502, recorder.Code)
								require.Equal(t, seamTestHTTPFail, recorder.Body.String())
								require.Equal(t, "7", recorder.Header().Get("Retry-After"))
							} else {
								require.Equal(t, success, recorder.Body.String())
							}
							if expired || slots == 0 {
								require.Zero(t, sends)
							}
						}
					}
				}
			}
		}
	}
}
