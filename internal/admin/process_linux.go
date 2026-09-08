//go:build linux

package admin

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

func processIdentity(pid int) (string, error) {
	data, e := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if e != nil {
		return "", e
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return "", errors.New("invalid process stat")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) <= 19 {
		return "", errors.New("invalid process stat")
	}
	if _, e = strconv.ParseUint(fields[19], 10, 64); e != nil {
		return "", e
	}
	boot, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		return "", e
	}
	return strings.TrimSpace(string(boot)) + ":" + fields[19], nil
}
