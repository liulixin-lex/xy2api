package service

// openAIWrappedUpstreamErrorStatus recognizes the two relay-wrapped error
// frame shapes and returns the upstream status they carry. sseEvent is the raw
// SSE `event:` value of the frame ("" when absent), payload its data JSON.
//
//   - wrapped:   one of the validated shapes; status is in 400–599.
//   - malformed: looks like a wrapped frame but fails validation; callers
//     treat it as an ordinary protocol failure and count it.
func openAIWrappedUpstreamErrorStatus(sseEvent string, payload []byte) (status int, wrapped bool, malformed bool) {
	return 0, false, false
}
