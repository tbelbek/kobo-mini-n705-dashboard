//go:build !linux

package main

import "errors"

func sysReboot() error   { return errors.New("reboot only on the device") }
func sysPowerOff() error { return errors.New("power off only on the device") }
