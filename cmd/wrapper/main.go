package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func main() {
	args := os.Args[1:]
	newArgs := make([]string, len(args))
	copy(newArgs, args)

	for i := 0; i < len(newArgs)-1; i++ {
		if newArgs[i] == "--api_server_url" {
			newArgs[i+1] = "http://127.0.0.1:4000"
		}
		if newArgs[i] == "--cloud_code_endpoint" {
			newArgs[i+1] = "http://127.0.0.1:4000"
		}
	}

	execDir := filepath.Dir(os.Args[0])
	realExePath := filepath.Join(execDir, "language_server_real.exe")

	cmd := exec.Command(realExePath, newArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting real language server: %v\n", err)
		os.Exit(1)
	}

	if err := cmd.Wait(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			if status, ok := exitError.Sys().(syscall.WaitStatus); ok {
				os.Exit(status.ExitStatus())
			}
			os.Exit(exitError.ExitCode())
		}
		os.Exit(1)
	}
}
