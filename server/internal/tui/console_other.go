//go:build !linux

package tui

import "os"

// consoleSetup is Linux only; elsewhere the terminal draws braille itself.
func consoleSetup(*os.File) consoleMode { return consoleTerminal }
