package service

import (
	"github.com/tidwall/gjson"
	"strings"
)

// Accept standalone JSON data lines without a blank separator, while retaining
// standard multiline JSON. This is the compatibility form required by E25.
type m7CompatSSEFrameParser struct{ openAICompatSSEFrameParser }

func (p *m7CompatSSEFrameParser) separates(line string) bool {
	data, ok := extractOpenAISSEDataLine(line)
	previous := strings.Join(p.dataLines, "\n")
	complete := func(value string) bool {
		return value == "[DONE]" || gjson.Valid(value) && gjson.Parse(value).IsObject()
	}
	return ok && complete(previous) && complete(data)
}

func (p *m7CompatSSEFrameParser) AddLine(line string) (openAICompatSSEFrame, bool) {
	if p.separates(line) {
		frame, ok := p.dispatch()
		p.openAICompatSSEFrameParser.AddLine(line)
		return frame, ok
	}
	return p.openAICompatSSEFrameParser.AddLine(line)
}
