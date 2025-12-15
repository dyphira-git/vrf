//go:build linux

package vrf

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func udsPeerTokenFromFD(fd int) (string, error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return "", err
	}

	if cred == nil {
		return "", fmt.Errorf("missing ucred")
	}

	if cred.Pid > 0 {
		return fmt.Sprintf("pid=%d", cred.Pid), nil
	}

	return fmt.Sprintf("uid=%d", cred.Uid), nil
}
