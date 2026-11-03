package main

import (
	"testing"
	"time"
)

// The alarm counts as set only with the driver's own line after our marker, close to d.
func TestAlarmTaken(t *testing.T) {
	const m = "kobodash: arming alarm 42"
	old := "[ 10.0] [msp430_setalarm-789] alarm 600\n"
	for _, c := range []struct {
		name, log string
		ok        bool
	}{
		{"taken", old + "[ 20.0] " + m + "\n[ 20.1] [msp430_setalarm-789] alarm 7192\n", true},
		{"dropped (no line after the marker)", old + "[ 20.0] " + m + "\n[ 20.1] read_alarm: 1 70/0/1 0:0:1\n", false},
		{"wrong interval", old + "[ 20.0] " + m + "\n[ 20.1] [msp430_setalarm-789] alarm 30\n", false},
		{"kernel that never logs alarms", "[ 20.0] " + m + "\n", true},
		{"marker rolled out of the buffer", old, true},
	} {
		err := alarmTaken(c.log, m, 7200*time.Second)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}
