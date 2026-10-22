//go:build !unix

package consume

import "os"

// openInput opens a file for reading.
func openInput(r *os.Root, name string) (file, error) {
	f, err := r.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	return f, nil
}
