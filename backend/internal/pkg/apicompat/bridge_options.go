package apicompat

// ToolNameIdentity identifies a tool independently of its bridge wire alias.
type ToolNameIdentity struct{ Kind, Namespace, Name string }
type BridgeToolNameMap struct {
	Forward map[ToolNameIdentity]string
	Reverse map[string]ToolNameIdentity
}

// BridgeOptions is an explicit path opt-in; the zero value retains legacy behavior.
type BridgeOptions struct {
	Enabled   bool
	ToolNames *BridgeToolNameMap
}
