//go:build unit

package service

// Request fixtures that carry call-argument ciphertext. Tests refer to these by
// name so that the JSON is written once.
const (
	// collabCallArgsDispatchCopy is a collaboration dispatch call whose
	// encrypted arguments carry the same ciphertext as collabBatchTask, so it
	// is a copy of that delivery.
	collabCallArgsDispatchCopy = `{"type":"function_call","id":"fc_copy","call_id":"call_copy","namespace":"collaboration","name":"send_message","arguments":"{\"message\":\"task\"}","encrypted_function_args":["message"]}`
	// collabCallArgsOnlyBody is a request whose only ciphertext is a call's
	// encrypted arguments.
	collabCallArgsOnlyBody = `{"input":[{"type":"function_call","id":"fc_a","name":"f","arguments":"{}","encrypted_function_args":["opaque"]}]}`
	// collabCallArgsRejection is an upstream rejection whose evidence names the
	// call arguments of collabCallArgsDispatchCopy.
	collabCallArgsRejection = `{"error":{"code":"invalid_encrypted_content","message":"Encrypted function arguments could not be verified for item fc_copy"}}`
	// collabTaskRejection is an upstream rejection whose evidence names the
	// task delivery of collabBatchTask.
	collabTaskRejection = `{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_task"}}`
)
