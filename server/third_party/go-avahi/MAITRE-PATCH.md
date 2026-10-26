# go-avahi, patched

Copy of github.com/holoplot/go-avahi v1.1.0 (MIT, see LICENSE) used through a
`replace` directive in `server/go.mod`.

One change, in `server.go` `dispatchLoop`: when the D-Bus signal channel is
closed, return instead of `continue`. Upstream spins on the closed channel at
100% of a core forever; godbus closes the channel whenever the connection
closes, including when the bus drops it (an avahi-daemon restart). Drop the
copy once upstream carries the fix.

Second change, `Server.Close`: the quit channel is closed once (sync.Once)
instead of sent to, so Close cannot block when the loop has already ended.

Second change, `Server.Close`: the quit channel is closed once (sync.Once)
instead of sent to, so Close cannot block when the loop has already ended.
