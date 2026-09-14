package service

import "net/http"

// API-key requests identify this application; OAuth continues its existing authenticated protocol.
func applyIQAPIKeyHeaders(h http.Header) { h.Set("User-Agent", "XY2API-IQ-Monitor/1") }
