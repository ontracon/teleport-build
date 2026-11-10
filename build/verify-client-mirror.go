// Verify the produced CDN package with Teleport's own platform-specific extractor.
// Run this file from the pinned Teleport source module on Windows or macOS.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gravitational/teleport/lib/utils/packaging"
)

func main() {
	if err := verify(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func verify() error {
	if len(os.Args) != 4 || (runtime.GOOS != "windows" && runtime.GOOS != "darwin") {
		return fmt.Errorf("usage: verify-client-mirror <archive> <new-directory> <version> (Windows or macOS)")
	}
	archive, destination, version := os.Args[1], os.Args[2], os.Args[3]
	names := []string{"tsh", "tctl"}
	if runtime.GOOS == "windows" {
		names = []string{"tsh.exe", "tctl.exe"}
		if err := os.Mkdir(destination, 0755); err != nil {
			return err
		}
	}
	paths, err := packaging.ReplaceToolsBinaries(archive, destination, names)
	if err != nil {
		return err
	}
	for _, name := range names {
		relative, ok := paths[name]
		if !ok {
			return fmt.Errorf("archive is missing %s", name)
		}
		command := exec.Command(filepath.Join(destination, relative), "version")
		command.Env = append(os.Environ(), "TELEPORT_TOOLS_VERSION=off")
		output, err := command.CombinedOutput()
		if err != nil || !strings.Contains(string(output), "v"+version+" ") {
			return fmt.Errorf("%s version check failed: %v: %s", name, err, output)
		}
	}
	fmt.Printf("Verified %s with Teleport's native client extractor\n", archive)
	return nil
}
