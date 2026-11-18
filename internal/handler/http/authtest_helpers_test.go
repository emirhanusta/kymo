package httphandler

import (
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/auth"
	"github.com/emirhanusta/kymo/internal/auth/authtest"
)

// disabledAuth is an auth service with authentication off (every caller is the
// local owner), for tests that exercise non-auth behaviour.
func disabledAuth() *auth.Service {
	s, err := auth.New(authtest.New(), auth.Options{Enabled: false}, nil)
	if err != nil {
		panic(err)
	}
	return s
}

func e0Log() *zap.Logger { return zap.NewNop() }

func (e *env) svcLog() *zap.Logger { return zap.NewNop() }
