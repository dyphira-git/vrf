//go:build !linux && !darwin

package vrf

import "fmt"

func udsPeerTokenFromFD(int) (string, error) {
	return "", fmt.Errorf("uds peer credentials unsupported on this platform")
}
