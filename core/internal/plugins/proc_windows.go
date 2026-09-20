//go:build windows

package plugins

import "os/exec"

func setProcAttr(cmd *exec.Cmd) {}
