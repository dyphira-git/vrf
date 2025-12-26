package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *exitCodeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func run(args []string) int {
	root := newRootCmd()
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		var ee *exitCodeError
		if errors.As(err, &ee) {
			if msg := strings.TrimSpace(ee.Error()); msg != "" {
				_, _ = fmt.Fprintln(os.Stderr, msg)
			}
			if ee.code != 0 {
				return ee.code
			}
			return 1
		}

		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}

	return 0
}
