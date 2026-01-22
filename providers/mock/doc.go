// Package mock provides a testify-based implementation of invoke.Environment
// for use in unit tests.
//
// Usage:
//
//	m := mock.New()
//	m.On("Run", ctx, mock.AnythingOfType("*invoke.Command")).
//	    Return(&invoke.Result{ExitCode: 0}, nil)
package mock
