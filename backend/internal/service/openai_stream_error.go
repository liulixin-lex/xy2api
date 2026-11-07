package service

import "github.com/tidwall/gjson"

// openAIStreamErrorObject reads a structured error envelope without mixing
// fields from different objects. Flat Responses error events are handled by
// openAIStreamErrorField only when neither envelope is an object.
func openAIStreamErrorObject(payload []byte) gjson.Result {
	root := gjson.ParseBytes(payload)
	if err := root.Get("response.error"); err.IsObject() {
		return err
	}
	if err := root.Get("error"); err.IsObject() {
		return err
	}
	return gjson.Result{}
}

func openAITopLevelStreamError(payload []byte) gjson.Result {
	root := gjson.ParseBytes(payload)
	if root.IsObject() && root.Get("type").String() == "error" &&
		!root.Get("response.error").IsObject() && !root.Get("error").IsObject() {
		return root
	}
	return gjson.Result{}
}

func openAIStreamErrorField(payload []byte, field string) gjson.Result {
	if object := openAIStreamErrorObject(payload); object.IsObject() {
		return object.Get(field)
	}
	if field == "type" {
		return gjson.Result{}
	} // top-level type names the event
	return openAITopLevelStreamError(payload).Get(field)
}
