// Package local provides an implementation of the invoke.Environment interface
// for the local operating system.
//
// It serves as a thin wrapper around the standard library's "os/exec" and "os"
// packages, adapting them to the unified invoke interfaces for Command execution
// and file transfer.
//
// Usage:
//
//	env, _ := local.New()
//	res, _ := env.Run(ctx, &invoke.Command{Cmd: "echo", Args: []string{"hello"}})
//	_ = res
package local
