package debugsurface

import "errors"

// ErrNotADebugBuild is what every procedure answers with in a shipped
// image: not "forbidden", which would imply a way in, but absent. Defined
// in both builds so a caller can tell the two answers apart.
var ErrNotADebugBuild = errors.New("this Node was not built with the debug console; rebuild the image with -tags maitredebug to use it")
