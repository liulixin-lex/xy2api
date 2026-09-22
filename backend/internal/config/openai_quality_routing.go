package config

import "fmt"

type OpenAIQualityRoutingRule struct {
	From string `mapstructure:"from" json:"from"`
	To   string `mapstructure:"to" json:"to"`
}

// Rules append to the built-in, explicit downgrade pairs.
type OpenAIQualityRoutingConfig struct {
	Mode         string                     `mapstructure:"mode"`
	AvoidSeconds int                        `mapstructure:"avoid_seconds"`
	Rules        []OpenAIQualityRoutingRule `mapstructure:"rules"`
}

func (c OpenAIQualityRoutingConfig) EffectiveMode() string {
	if c.Mode == "" {
		return "enforce"
	}
	return c.Mode
}

func (c OpenAIQualityRoutingConfig) EffectiveAvoidSeconds() int {
	if c.AvoidSeconds == 0 {
		return 300
	}
	return c.AvoidSeconds
}

func (c OpenAIQualityRoutingConfig) Validate() error {
	switch c.EffectiveMode() {
	case "off", "observe", "enforce":
	default:
		return fmt.Errorf("gateway.openai_quality_routing.mode must be off, observe or enforce")
	}
	if c.AvoidSeconds < 0 || c.AvoidSeconds > 86400 {
		return fmt.Errorf("gateway.openai_quality_routing.avoid_seconds must be between 0 and 86400")
	}
	for _, r := range c.Rules {
		if r.From == "" || r.To == "" || r.From == r.To {
			return fmt.Errorf("gateway.openai_quality_routing.rules require distinct nonempty from/to models")
		}
	}
	return nil
}
