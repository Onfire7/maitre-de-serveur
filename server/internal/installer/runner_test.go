package installer

import (
	"errors"
	"fmt"
	"testing"
)

func TestTailWriterExplainsAFailure(t *testing.T) {
	tw := &tailWriter{}
	fmt.Fprint(tw, "Checking that no-one is using this disk right now ... OK\n\nsfdisk: All space for primary partitions is in use.\r\nsfdisk: Failed to add #4 partition: Numerical result out of range\n")
	err := tw.explain(errors.New("exit status 1"))
	want := "exit status 1: Checking that no-one is using this disk right now ... OK / sfdisk: All space for primary partitions is in use. / sfdisk: Failed to add #4 partition: Numerical result out of range"
	if err.Error() != want {
		t.Errorf("got %q", err)
	}
	if (&tailWriter{}).explain(nil) != nil {
		t.Error("success must stay nil")
	}
	if err := (&tailWriter{}).explain(errors.New("exit status 2")); err.Error() != "exit status 2" {
		t.Errorf("no output: %q", err)
	}
}
