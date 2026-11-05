//go:build !maitredebug

// Package debugsurface is decision 323 and 326: the remote diagnostics and
// command execution a Debug Console uses, present only in an image built
// for it.
//
// A build tag and not a runtime setting, deliberately. The procedures are
// absent from a shipped binary rather than guarded inside one, so a
// compromised Console has nothing to switch on and nothing to find. This
// file is what a shipped Node contains: the answer, and no mechanism.
//
// What this tier holds is the dangerous half — reading an arbitrary file,
// running a command — reachable over the network by a paired Debug
// Console. The other two tiers are elsewhere by design: the basic
// diagnostics that are not a security surface (filesystem usage, the image
// list, a unit's journal, prune) are in every build for a Trusted Device,
// which can already install Services and open their pages, and a shell
// lives only in a rescue boot, where networking is off and physical
// presence is already assumed.
package debugsurface

import "context"

// Enabled reports whether this binary carries the debug surface. False
// here, and the Console turns the whole image box amber when it is true.
const Enabled = false

// Exec runs a command. Never, in this build.
func Exec(context.Context, string, []string) (string, error) { return "", ErrNotADebugBuild }

// ReadFile reads a file under the allowlist. Never, in this build.
func ReadFile(string) ([]byte, error) { return nil, ErrNotADebugBuild }
