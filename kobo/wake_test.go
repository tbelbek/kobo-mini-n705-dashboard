package main

import "testing"

// /proc/interrupts as the N705 prints it (3 Nov), and what each change in it means.
const irqTable = `           CPU0
  2:     486632    MXC_TZIC  mmc1
 14:          0    MXC_TZIC  usb_wakeup, ehci_hcd:usb1
 18:         17    MXC_TZIC  usb_wakeup, fsl-usb2-udc
 60:          0    MXC_TZIC  mxckpd
234:          0           -  power_key
235:         28           -  msp_int
243:          6           -  acin_pg
271:        157           -  zForce-ir-touch
Err:          0
`

func TestWakeIRQs(t *testing.T) {
	if key, usb := wakeIRQs(irqTable); key != 0 || usb != 23 {
		t.Fatalf("key %d usb %d, want 0 23", key, usb)
	}
	for _, c := range []struct {
		at             bool
		k0, u0, k1, u1 int64
		want           string
	}{
		{true, 0, 0, 0, 0, "timer"},
		{false, 0, 23, 1, 23, "button"},
		{false, 0, 23, 0, 25, "usb"},    // the charger plug: straight back to sleep
		{false, 0, 23, 1, 25, "button"}, // a press with the plug: a person
		{false, 0, 23, 0, 23, "button"}, // can't tell: stay up for a person
	} {
		if got := wokeBy(c.at, c.k0, c.u0, c.k1, c.u1); got != c.want {
			t.Errorf("%+v: %s, want %s", c, got, c.want)
		}
	}
}
