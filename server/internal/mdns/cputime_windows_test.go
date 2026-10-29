package mdns

import "time"

// cpuTime is not measured on Windows, where Avahi does not run anyway.
func cpuTime() time.Duration { return 0 }
