package main

// Suspend-to-RAM with a timed wake-up through the RTC alarm.
// The Mini's kernel has no /sys/class/rtc/rtc0/wakealarm (so rtcwake refuses),
// but the alarm may still be settable through /dev/rtc0 ioctls, the way KOReader does it.

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"syscall"
	"time"
	"unsafe"
)

const (
	rtcRdTime   = 0x80247009 // _IOR('p', 0x09, struct rtc_time)   36 bytes
	rtcWkalmSet = 0x4028700f // _IOW('p', 0x0f, struct rtc_wkalrm) 40 bytes
	rtcWkalmRd  = 0x80287010 // _IOR('p', 0x10, struct rtc_wkalrm)
)

func ioctl(fd uintptr, req uintptr, buf []byte) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
		return e
	}
	return nil
}

// struct rtc_time: 9 ints (sec min hour mday mon year wday yday isdst); RTC keeps UTC.
func decodeRTC(b []byte) time.Time {
	v := func(i int) int { return int(int32(binary.LittleEndian.Uint32(b[i*4:]))) }
	return time.Date(v(5)+1900, time.Month(v(4)+1), v(3), v(2), v(1), v(0), 0, time.UTC)
}

func encodeRTC(b []byte, t time.Time) {
	t = t.UTC()
	for i, v := range []int{t.Second(), t.Minute(), t.Hour(), t.Day(), int(t.Month()) - 1,
		t.Year() - 1900, int(t.Weekday()), t.YearDay() - 1, 0} {
		binary.LittleEndian.PutUint32(b[i*4:], uint32(int32(v)))
	}
}

// setWakeAlarm arms the RTC alarm d from now and returns the alarm read back.
func setWakeAlarm(d time.Duration) (time.Time, error) {
	f, err := os.Open("/dev/rtc0")
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()
	now := make([]byte, 36)
	if err := ioctl(f.Fd(), rtcRdTime, now); err != nil {
		return time.Time{}, fmt.Errorf("RTC_RD_TIME: %w", err)
	}
	wk := make([]byte, 40) // u8 enabled, u8 pending, 2 pad, struct rtc_time
	wk[0] = 1
	encodeRTC(wk[4:], decodeRTC(now).Add(d))
	if err := ioctl(f.Fd(), rtcWkalmSet, wk); err != nil {
		return time.Time{}, fmt.Errorf("RTC_WKALM_SET: %w", err)
	}
	back := make([]byte, 40)
	if err := ioctl(f.Fd(), rtcWkalmRd, back); err != nil {
		return time.Time{}, fmt.Errorf("RTC_WKALM_RD: %w", err)
	}
	if back[0] == 0 {
		return decodeRTC(back[4:]), fmt.Errorf("alarm not enabled after set")
	}
	return decodeRTC(back[4:]), nil
}

// suspend: Kobo sequence (Nickel/KOReader): state-extended=1, settle, mem; blocks until resume.
func suspend() error {
	if err := os.WriteFile("/sys/power/state-extended", []byte("1"), 0644); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	err := os.WriteFile("/sys/power/state", []byte("mem"), 0644)
	os.WriteFile("/sys/power/state-extended", []byte("0"), 0644)
	return err
}

func suspendTest(sec int) {
	log.SetOutput(os.Stdout)
	alarm, err := setWakeAlarm(time.Duration(sec) * time.Second)
	if err != nil {
		log.Printf("alarm: %v (read back %v)", err, alarm)
		return
	}
	log.Printf("alarm armed for %v UTC (in %ds); Wi-Fi off, suspending", alarm.Format("15:04:05"), sec)
	wifiDown()
	start := time.Now().Round(0) // wall clock: monotonic time stops during suspend
	err = suspend()
	slept := time.Now().Round(0).Sub(start)
	log.Printf("resumed after %v (suspend err: %v) — woke by itself if this is ~%ds", slept.Round(time.Second), err, sec)
	reconnectWifi()
}
