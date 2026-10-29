package devices

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestList(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"ttyUSB0", "ttyUSB1", "ttyACM0", "dri/renderD128", "dri/card0", "sda"} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755)
		os.WriteFile(filepath.Join(root, f), nil, 0o644)
	}
	os.MkdirAll(filepath.Join(root, "serial", "by-id"), 0o755)
	os.Symlink("../../ttyUSB0", filepath.Join(root, "serial", "by-id", "usb-Sonoff_Zigbee-if00-port0"))
	got := List(root, "")
	want := []Device{
		{"/dev/serial/by-id/usb-Sonoff_Zigbee-if00-port0", "serial", "usb-Sonoff_Zigbee-if00-port0"},
		{"/dev/ttyACM0", "serial", "ttyACM0"},
		{"/dev/ttyUSB1", "serial", "ttyUSB1"},
		{"/dev/dri/renderD128", "video", "GPU 128"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List:\n%v\nwant\n%v", got, want)
	}
	if v := List(root, "video"); len(v) != 1 || v[0].Kind != "video" {
		t.Errorf("video: %v", v)
	}
}
