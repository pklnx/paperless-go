//go:build unix

package consume

import (
	"os"
	"syscall"
)

// openInput opens a file for reading. O_NONBLOCK keeps a FIFO that
// replaced the checked regular file from blocking the scan; the caller
// then sees from Stat that it is not the checked file.
func openInput(r *os.Root, name string) (file, error) {
	f, err := r.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return f, nil
}
