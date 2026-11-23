//go:build unit

package resilience

import (
	"bytes"
	"github.com/tidwall/gjson"
	"strings"
	"unsafe"
)

// Unfiltered reference preserves the original parser for differential tests.
func collabIngressUnfilteredReference(body []byte) (out CollabIngressObservation) {
	defer func() {
		if recover() != nil {
			out = CollabIngressObservation{}
		}
	}()
	body = bytes.TrimSpace(body)
	if len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
		return out
	}
	// The view and all gjson results are local to this read-only call; none
	// escape or outlive body. Like the request replay parser, avoid GetBytes
	// and ParseBytes, which copy raw input/results proportional to body size.
	root := gjson.Parse(unsafe.String(unsafe.SliceData(body), len(body)))
	if root.Get("type").String() == "response.create" && root.Get("response").IsObject() {
		root = root.Get("response")
	}
	if !root.IsObject() {
		return out
	}
	out.Observed = true
	out.Carrier = "none"
	top, additional := false, false
	inspectTools := func(tools gjson.Result) bool {
		found := false
		tools.ForEach(func(_, tool gjson.Result) bool {
			name := tool.Get("name").String()
			if name == "" {
				name = tool.Get("function.name").String()
			}
			if tool.Get("type").String() == "namespace" {
				if name == "collaboration" {
					found = true
				} else {
					tool.Get("tools").ForEach(func(_, member gjson.Result) bool {
						switch member.Get("name").String() {
						case "spawn_agent", "send_message", "followup_task":
							out.SuspectedCustomNamespace = true
						}
						return true
					})
				}
			}
			if strings.HasPrefix(name, "collaboration.") || tool.Get("namespace").String() == "collaboration" {
				found = true
			}
			return true
		})
		return found
	}
	top = inspectTools(root.Get("tools"))
	root.Get("input").ForEach(func(_, item gjson.Result) bool {
		switch item.Get("type").String() {
		case "additional_tools":
			additional = inspectTools(item.Get("tools")) || additional
		case "agent_message":
			item.Get("content").ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "encrypted_content" || part.Get("encrypted_content").Exists() {
					out.EncryptedAgent = true
				}
				return true
			})
		case "function_call_output", "tool_output":
			if item.Get("encrypted_content").Exists() {
				out.EncryptedTool = true
			}
			item.Get("output").ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "encrypted_content" || part.Get("encrypted_content").Exists() {
					out.EncryptedTool = true
				}
				return true
			})
		}
		if item.Get("role").String() == "developer" {
			content := item.Get("content")
			check := func(text string) {
				if strings.Contains(text, "collaboration") && strings.Contains(text, "namespace") && !strings.Contains(text, "namespace collaboration") {
					out.UnrecognizedCatalog = true
				}
			}
			if content.Type == gjson.String {
				check(content.String())
			} else {
				content.ForEach(func(_, part gjson.Result) bool { check(part.Get("text").String()); return true })
			}
		}
		return true
	})
	out.Declared = top || additional
	switch {
	case top && additional:
		out.Carrier = "both"
	case top:
		out.Carrier = "tools"
	case additional:
		out.Carrier = "additional_tools"
	}
	return out
}
