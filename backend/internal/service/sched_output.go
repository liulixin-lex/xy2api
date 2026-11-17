package service

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// observeOutput is called on upstream payloads before conversion or delivery.
// A converter may suppress reasoning, so the downstream writer cannot own the
// account's output clocks. Nonstream attempts never produce first-output data.
func (a *schedAttemptCapture) observeOutput(payload []byte, at time.Time, sourceEvent ...string) resilience.OutputClass {
	event := ""
	if len(sourceEvent) > 0 {
		event = sourceEvent[0]
	}
	output := resilience.ClassifyOutput("", event, payload)
	if a != nil && a.stream {
		if output.Valid {
			a.MarkOutput(OutputValid, at)
		}
		if output.Answer {
			a.MarkOutput(OutputAnswer, at)
		}
	}
	return output
}

// schedFirstEvent preserves the usage log's existing TTFT value and decision,
// including openai_ttft_mode and relay keepalive adjustments. It is evaluated
// before the forwarding result is handed to the attempt's Finish owner.
func schedFirstEvent(c *gin.Context, start time.Time, ms *int) *int {
	if c != nil && c.Request != nil && ms != nil {
		markSchedFirstEvent(currentSchedAttempt(c.Request.Context()), start, ms)
	}
	return ms
}

func markSchedFirstEvent(a *schedAttemptCapture, start time.Time, ms *int) *int {
	if a != nil && a.stream && ms != nil {
		a.MarkOutput(OutputFirstEvent, start.Add(time.Duration(*ms)*time.Millisecond))
	}
	return ms
}

// openAIStreamCommitsOutput preserves legacy commitment when operators disable
// the master or native whitelist setting. Compatibility-only prelude staging
// does not control native Responses or passthrough commitment.
func openAIStreamCommitsOutput(event string, payload []byte, terminal OpenAIResponsesTerminal) bool {
	settings := resilience.Current()
	if !settings.Enabled {
		return openAIStreamDataStartsClientOutput(string(payload), event)
	}
	if !resilience.Current().NativePreludeWhitelistOn() {
		return openAIStreamDataStartsClientOutput(string(payload), event)
	}
	output := resilience.ClassifyOutput("", event, payload)
	if !output.Known {
		return openAIStreamDataStartsClientOutput(string(payload), event)
	}
	return output.Valid || terminal.Valid || strings.TrimSpace(string(payload)) == "[DONE]"
}
