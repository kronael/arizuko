package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func hasInstanceFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "-i" || arg == "--instance" || strings.HasPrefix(arg, "-i=") || strings.HasPrefix(arg, "--instance=") {
			return true
		}
	}
	return false
}

func cmdRm(args []string) {
	args = need(args, 1, 1, "arizuko rm -i NAME")
	if err := removeInstance(args[0]); err != nil {
		die("Failed: %v", err)
	}
}

func removeInstance(name string) error {
	if name == "" {
		return errors.New("rm requires an instance name")
	}
	dir, err := instanceDir(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err != nil {
		return err
	}
	unit := "arizuko_" + name
	cmd := exec.Command("systemctl", "is-active", "--quiet", unit)
	if err := cmd.Run(); err == nil {
		return fmt.Errorf("%s is active", unit)
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || (exit.ExitCode() != 3 && exit.ExitCode() != 4) {
			return fmt.Errorf("check %s: %w", unit, err)
		}
	}
	compose := filepath.Join(dir, "docker-compose.yml")
	if _, err := os.Stat(compose); err == nil {
		cmd := exec.Command("docker", "compose", "-p", unit, "-f", compose, "down", "-v", "--remove-orphans")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("compose down: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	env, err := view(dir)
	if err != nil {
		return err
	}
	if env["DYNAMODB_URL"] != "" {
		m, err := newManager(env)
		if err != nil {
			return err
		}
		if err := deleteStore(dir, unit, m); err != nil {
			return err
		}
	}
	return os.RemoveAll(dir)
}
