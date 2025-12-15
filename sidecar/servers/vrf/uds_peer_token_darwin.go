//go:build darwin

package vrf

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func udsPeerTokenFromFD(fd int) (string, error) {
	pid, pidErr := unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	if pidErr == nil && pid > 0 {
		return fmt.Sprintf("pid=%d", pid), nil
	}

	cred, credErr := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if credErr == nil && cred != nil {
		return fmt.Sprintf("uid=%d", cred.Uid), nil
	}

	if pidErr != nil {
		return "", pidErr
	}
	if credErr != nil {
		return "", credErr
	}

	return "", fmt.Errorf("unable to determine peer credentials")
}
