package service

import "encoding/json"

// mustJSONString 把一个 Go string 序列化为合法 JSON string（含引号），
// 用于 sjson.SetRawBytes 场景下手工拼 JSON。
func mustJSONString(s string) string {
	// Go string quoting can emit non-JSON escapes such as \x7f or \a.
	// Marshaling a string cannot fail.
	encoded, _ := json.Marshal(s)
	return string(encoded)
}
