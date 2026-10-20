// Package sysutil provides system-level utilities for process management.
package sysutil

import (
	"log"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// restartHook is installed by the running server (xy2api): instead of exiting
// at once, the server stops accepting requests, drains the ones in flight and
// then exits, so the supervisor starts the new binary.
var restartHook atomic.Pointer[func()]

// SetRestartHook installs the graceful restart of the running server; nil
// restores the immediate exit (setup wizard).
func SetRestartHook(fn func()) {
	if fn == nil {
		restartHook.Store(nil)
		return
	}
	restartHook.Store(&fn)
}

// ContainerNotice is logged and returned by online updates inside a
// container: the update replaces the binary in the container's writable
// layer, which a recreated container does not keep.
const ContainerNotice = "Running in a container: update the image instead. An online update only replaces the binary inside this container and is lost when the container is recreated."

// InContainer reports whether the process runs in a container (Docker,
// Podman, Kubernetes).
func InContainer() bool {
	return inContainer(fileExists, os.ReadFile, os.Getenv)
}

func inContainer(exists func(string) bool, read func(string) ([]byte, error), getenv func(string) string) bool {
	if exists("/.dockerenv") || exists("/run/.containerenv") || getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}
	if v := strings.TrimSpace(getenv("container")); v != "" {
		return true
	}
	cgroup, err := read("/proc/1/cgroup")
	if err != nil {
		return false
	}
	text := string(cgroup)
	for _, marker := range []string{"docker", "containerd", "kubepods", "libpod"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// RestartService triggers a service restart by gracefully exiting.
//
// This relies on systemd's Restart=always configuration to automatically
// restart the service after it exits. This is the industry-standard approach:
//   - Simple and reliable
//   - No sudo permissions needed
//   - No complex process management
//   - Leverages systemd's native restart capability
//
// Prerequisites:
//   - Linux OS with systemd
//   - Service configured with Restart=always in systemd unit file
func RestartService() error {
	if runtime.GOOS != "linux" {
		log.Println("Service restart via exit only works on Linux with systemd")
		return nil
	}

	return restart()
}

func restart() error {
	if fn := restartHook.Load(); fn != nil {
		log.Println("Initiating service restart: draining in-flight requests before exit...")
		go (*fn)()
		return nil
	}

	log.Println("Initiating service restart by graceful exit...")
	log.Println("systemd will automatically restart the service (Restart=always)")

	// Give a moment for logs to flush and response to be sent
	go func() {
		time.Sleep(100 * time.Millisecond)
		os.Exit(0)
	}()

	return nil
}

// RestartServiceAsync is a fire-and-forget version of RestartService.
// It logs errors instead of returning them, suitable for goroutine usage.
func RestartServiceAsync() {
	if err := RestartService(); err != nil {
		log.Printf("Service restart failed: %v", err)
		log.Println("Please restart the service manually: sudo systemctl restart xy2api")
	}
}
