package handler

import "sync"

// The same once-protected cleanup belongs to the stack until explicitly
// transferred to a request or turn. An outcome's zero value never transfers it.
type admissionReleaseGuard struct {
	release     func()
	transferred bool
}

func newAdmissionReleaseGuard(release func()) *admissionReleaseGuard {
	return &admissionReleaseGuard{release: sync.OnceFunc(func() {
		if release != nil {
			release()
		}
	})}
}
func (g *admissionReleaseGuard) cleanup() {
	if !g.transferred {
		g.release()
	}
}
func (g *admissionReleaseGuard) transfer() func() {
	g.transferred = true
	return g.release
}
