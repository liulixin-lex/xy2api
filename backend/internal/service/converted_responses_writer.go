package service

import "github.com/gin-gonic/gin"

const convertedResponsesErrorStateKey = "xy_converted_responses_error_state"

// Converted streams can be used without the handler's delivery wrapper. Keep
// the same envelope observer in that case so failure preserves the emitted ID.
func convertedResponsesErrorState(c *gin.Context) *targetProtocolErrorState {
	if w := deliveryWriterFrom(c); w != nil {
		return &w.terminal
	}
	if value, ok := c.Get(convertedResponsesErrorStateKey); ok {
		return value.(*targetProtocolErrorState)
	}
	state := &targetProtocolErrorState{}
	c.Set(convertedResponsesErrorStateKey, state)
	return state
}

func writeConvertedResponsesFrame(c *gin.Context, frame string) (int, error) {
	n, err := c.Writer.Write([]byte(frame))
	if n > 0 && deliveryWriterFrom(c) == nil {
		convertedResponsesErrorState(c).observe(c.Writer.Header().Get("Content-Type"), []byte(frame[:n]))
	}
	return n, err
}
