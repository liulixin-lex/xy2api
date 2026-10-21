//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDeliveryObservationStartsAtContent(t *testing.T) {
	c, st := newSSEDeliveryContext(t, "text/event-stream")
	_, err := c.Writer.WriteString("event: response.created\ndata: {\"type\":\"response.created\"}\n\n")
	require.NoError(t, err)
	require.Zero(t, st.MSSinceFirstOutput(time.Now().Add(time.Second)))
	_, err = c.Writer.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi\"}\n\n"))
	require.NoError(t, err)
	require.Positive(t, st.MSSinceFirstOutput(time.Now().Add(time.Second)))
	require.Positive(t, st.DeliveredBytes())
}

func TestDeliveryObservationIgnoresJSONHeartbeats(t *testing.T) {
	c, st := newSSEDeliveryContext(t, "application/json")
	stop := StartOpenAIImagesJSONKeepalive(c, time.Hour)
	defer stop()
	require.True(t, openAIImagesJSONKeepaliveFromContext(c).beat())
	require.Zero(t, st.DeliveredBytes())
	require.Zero(t, st.MSSinceFirstOutput(time.Now().Add(time.Second)))
	_, err := c.Writer.WriteString(" \n")
	require.NoError(t, err)
	require.Zero(t, st.MSSinceFirstOutput(time.Now().Add(time.Second)))
	_, err = c.Writer.WriteString(`{"data":[]}`)
	require.NoError(t, err)
	require.Positive(t, st.MSSinceFirstOutput(time.Now().Add(time.Second)))
}

func TestDeliveryObservationStartsAtSplitBlockEnd(t *testing.T) {
	c, st := newSSEDeliveryContext(t, "text/event-stream")
	_, err := c.Writer.WriteString("data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi\"}")
	require.NoError(t, err)
	require.Zero(t, st.MSSinceFirstOutput(time.Now().Add(time.Second)))
	_, err = c.Writer.WriteString("\n\n")
	require.NoError(t, err)
	require.Positive(t, st.MSSinceFirstOutput(time.Now().Add(time.Second)))
}
