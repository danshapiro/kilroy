package procutil

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ProcFSAvailable reports whether procfs is available for process introspection.
func ProcFSAvailable() bool {
	_, err := os.Stat("/proc/self/stat")
	return err == nil
}

// PIDAlive reports whether a process exists and is not a zombie.
func PIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if PIDZombie(pid) {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

// PIDZombie checks whether a PID is in a zombie/dead state.
func PIDZombie(pid int) bool {
	if !ProcFSAvailable() {
		return pidZombieFromPS(pid)
	}
	state, _, err := readProcStat(pid)
	if err != nil {
		return false
	}
	return state == 'Z' || state == 'X'
}

// ReadPIDStartTime returns the kernel process start-time tick value from
// /proc/<pid>/stat field 22 (1-indexed).
func ReadPIDStartTime(pid int) (uint64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid %d", pid)
	}
	_, startTime, err := readProcStat(pid)
	if err != nil {
		return 0, err
	}
	return startTime, nil
}

func readProcStat(pid int) (byte, uint64, error) {
	statPath := filepath.Join("/proc", strconv.Itoa(pid), "stat")
	b, err := os.ReadFile(statPath)
	if err != nil {
		return 0, 0, err
	}
	return parseProcStatLine(string(b))
}

func parseProcStatLine(line string) (byte, uint64, error) {
	closeIdx := strings.LastIndexByte(line, ')')
	if closeIdx < 0 || closeIdx+2 >= len(line) {
		return 0, 0, fmt.Errorf("malformed stat record")
	}
	state := line[closeIdx+2]
	fields := strings.Fields(line[closeIdx+2:])
	if len(fields) < 20 {
		return 0, 0, fmt.Errorf("malformed stat fields")
	}
	// fields[0] is state (field 3 in /proc/<pid>/stat); therefore starttime
	// (field 22, 1-indexed) maps to fields[19].
	startTime, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	return state, startTime, nil
}

func pidZombieFromPS(pid int) bool {
	out, err := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	state := strings.TrimSpace(string(out))
	if state == "" {
		return false
	}
	c := state[0]
	return c == 'Z' || c == 'X'
}
