//go:build !unix

package process

import "os"

// Without flock, no other process's work directory counts as stale.
func lockFile(*os.File) error { return nil }

func tryLockFile(*os.File) (bool, error) { return false, nil }
