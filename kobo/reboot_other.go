//go:build !linux

package main

import "errors"

func sysReboot() error { return errors.New("reboot only on the device") }
