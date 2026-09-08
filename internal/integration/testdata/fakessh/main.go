// The standalone SSH fixture execs only the explicitly supplied production
// bridge binary. It is built by tests and never shipped in agent_romm.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func main() {
	if len(os.Args) != 4 {
		os.Exit(2)
	}
	binary, home, pidfile := os.Args[1], os.Args[2], os.Args[3]
	if !filepath.IsAbs(binary) || !filepath.IsAbs(home) || !filepath.IsAbs(pidfile) {
		os.Exit(2)
	}
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		os.Exit(1)
	}
	// UserConfigDir is the ordinary per-account locator, not a dependency bypass.
	var env []string
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "HOME=") && !strings.HasPrefix(value, "XDG_CONFIG_HOME=") {
			env = append(env, value)
		}
	}
	env = append(env, "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))
	path, err := exec.LookPath(binary)
	if err != nil {
		os.Exit(1)
	}
	if err = syscall.Exec(path, []string{binary, "bridge"}, env); err != nil {
		os.Exit(1)
	}
}
