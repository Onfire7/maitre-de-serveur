// Package layout says where a Service's own folders live in a Space's Pool
// (decision 205): <Space>/.Services/<Service>/<volume>, out of the way of
// the user's own folders at the top of the Space. The Files tab hides
// names starting with a dot unless asked.
package layout

import "path/filepath"

// ServicesDir is the folder at the top of each Space holding Services'
// own folders.
const ServicesDir = ".Services"

// ServiceDir is a Service's folder, relative to its Space's Pool.
func ServiceDir(service string) string { return filepath.Join(ServicesDir, service) }

// VolumeDir is a Service's own folder for one volume, relative to its
// Space's Pool.
func VolumeDir(service, volume string) string { return filepath.Join(ServicesDir, service, volume) }

// LegacyVolumeDir is where a volume lived before decision 205,
// <Space>/<Service>/<volume>. Only MoveVolume still looks there, so that
// moving a volume to another Space (and back) brings an older Service's
// data to the new place.
func LegacyVolumeDir(service, volume string) string { return filepath.Join(service, volume) }
