//go:build unit

package service_test

import (
	"testing"

	"github.com/liulixin-lex/xy2api/internal/repository"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func TestOwnerGuardRedirectTimeout(t *testing.T) {
	service.CheckOwnerGuardRedirectTimeout(t, repository.NewHTTPUpstream(nil))
}
