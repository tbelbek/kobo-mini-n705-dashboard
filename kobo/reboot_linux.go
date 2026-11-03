package main

import "syscall"

func sysReboot() error { return syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART) }
