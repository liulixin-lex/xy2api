package config

// MCPConfig uses credentials independent of browser JWT and gateway API keys.
// ReaderDSN must identify a different, SELECT-only PostgreSQL login.
type MCPConfig struct {
	Enabled         bool     `mapstructure:"enabled"`
	PublicURL       string   `mapstructure:"public_url"`
	OAuthHMACSecret string   `mapstructure:"oauth_hmac_secret"`
	ReaderDSN       string   `mapstructure:"reader_dsn"`
	AllowedOrigins  []string `mapstructure:"allowed_origins"`
}
