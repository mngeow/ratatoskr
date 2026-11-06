//go:build linux

package daemon

// Linux ancestry: /proc/<pid>/stat.

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// parentOf returns the parent PID of pid.
//
// The comm field may contain spaces and parentheses, so the parse starts AFTER the LAST
// ')' in the line: the first token there is the state, the second is the ppid. Anything
// else is an incomplete read, which the caller treats as "could not check".
func parentOf(pid int) (int, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, fmt.Errorf("daemon: read the process table: %w", err)
	}
	return parseStatPPID(string(data))
}

// parseStatPPID is the /proc/<pid>/stat parse, without the file read.
func parseStatPPID(line string) (int, error) {
	end := strings.LastIndexByte(line, ')')
	if end < 0 || end+1 >= len(line) {
		return 0, errors.New("daemon: the process table entry has no comm field")
	}

	fields := strings.Fields(line[end+1:])
	// fields[0] is the state; fields[1] is the ppid.
	if len(fields) < 2 {
		return 0, errors.New("daemon: the process table entry is too short")
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil || ppid < 0 {
		return 0, errors.New("daemon: the process table entry has no usable parent")
	}
	return ppid, nil
}
