package pool

// Pools are assembled only on a Node (Linux); this keeps the Console's
// helper cross-compiling for Windows. A branch it cannot read is ordered last.
func statBranch(string) (uint64, float64, bool) { return 0, 0, false }
