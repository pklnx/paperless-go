// Package consume imports the files users drop into the consume folder.
//
// The consume folder has one subfolder per user, named exactly like the
// user (consume/<username>/). The consumer polls it: a regular file
// directly in a user's folder is imported for that user once its size and
// modification time stayed the same across two scans at least Settle
// apart. An imported file, or one the user already has (a duplicate), is
// deleted, but only after the document row is committed and only while
// the file is still the one that was read. A file the importer refuses
// (unsupported, too large, unreadable as its type) moves to the user's
// failed/ folder with a <name>.error.txt sidecar. Anything else, such as a
// database or blob store error, leaves the file in place for the next
// scan.
//
// Everything below the consume folder is reached through an os.Root, so
// no path leaves the tree, and every entry is checked with Lstat: only
// real directories count as user folders and only regular files as
// inputs. Symbolic links are never followed; they are skipped, left in
// place and logged once.
package consume

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pklnx/paperless-go/internal/ingest"
	"github.com/pklnx/paperless-go/internal/store"
)

// Defaults of the polling.
const (
	// DefaultInterval is how often serve scans the consume folder.
	DefaultInterval = 10 * time.Second
	// DefaultSettle is how long a file's size and modification time must
	// stay unchanged before it is imported.
	DefaultSettle = 2 * time.Second
)

// FailedDir is the folder in each user's folder that takes refused files.
const FailedDir = "failed"

// ErrorSuffix is appended to a refused file's name for its sidecar, which
// says why the file was refused.
const ErrorSuffix = ".error.txt"

// maxSuffix bounds the number tried for a free name in failed/.
const maxSuffix = 9999

// HoldTime is how long a file that was read but could not be deleted or
// moved (a folder that is not writable, failed/ that is not a folder)
// waits before it is tried again, unless it changes. Without it, the
// file would be read and stored again at every scan.
const HoldTime = time.Hour

// IncompleteTime is how long a PDF, JPEG or PNG without its end marker
// waits, unchanged, before it is imported anyway: the writer may only
// have paused, but some real files lack the marker.
const IncompleteTime = 10 * time.Minute

// maxNameBytes is the longest file name most file systems allow.
const maxNameBytes = 255

// Users is the part of *store.Store the consumer uses.
type Users interface {
	ListUsers(ctx context.Context) ([]*store.User, error)
}

// Importer imports one file; *ingest.Importer implements it.
type Importer interface {
	Import(ctx context.Context, req ingest.Request) (ingest.Result, error)
}

// Options configure a Consumer. The zero value uses the defaults.
type Options struct {
	Log *slog.Logger
	// Now is the clock; time.Now if nil.
	Now func() time.Time
	// Settle is how long a file must stay unchanged; DefaultSettle if 0.
	Settle time.Duration
}

// Consumer scans one consume folder. Scan and Run must not be called
// concurrently.
type Consumer struct {
	dir    string
	users  Users
	im     Importer
	log    *slog.Logger
	now    func() time.Time
	settle time.Duration
	open   func(r *os.Root, name string) (file, error)
	// openRoot opens a user's folder; tests replace it.
	openRoot func(r *os.Root, name string) (*os.Root, error)

	// seen is the last observation of each candidate file.
	seen map[fileKey]observation
	// visited are the candidates found by the current scan.
	visited map[fileKey]bool
	// partial are the users whose folder the current scan left early.
	partial map[int64]bool
	// warned holds the problems already logged, with the number of the
	// last scan that found them; a problem that is gone is forgotten, so
	// it is logged again if it comes back.
	warned  map[string]int
	scanNo  int
	rootErr string
	scanErr string // the last error of Run's scans, logged once
}

type fileKey struct {
	user int64
	name string
}

// observation is a file's state when it was last seen changed.
type observation struct {
	info fs.FileInfo
	at   time.Time
	// held: read, but it could not be deleted or moved; it waits for
	// HoldTime or a change.
	held bool
}

// file is an opened input; *os.File implements it.
type file interface {
	io.Reader
	io.ReaderAt
	Stat() (fs.FileInfo, error)
	Close() error
}

// New returns a consumer of the consume folder dir.
func New(dir string, users Users, im Importer, opts Options) *Consumer {
	c := &Consumer{
		dir: dir, users: users, im: im, log: opts.Log, now: opts.Now, settle: opts.Settle,
		open:     openInput,
		openRoot: func(r *os.Root, name string) (*os.Root, error) { return r.OpenRoot(name) },
		seen:     make(map[fileKey]observation),
		visited:  make(map[fileKey]bool),
		partial:  make(map[int64]bool),
		warned:   make(map[string]int),
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.settle <= 0 {
		c.settle = DefaultSettle
	}
	return c
}

// Run scans at once and then at every tick until ctx ends. An error stops
// only the scan it happened in; the next tick scans again.
func (c *Consumer) Run(ctx context.Context, tick <-chan time.Time) {
	for {
		_, err := c.Scan(ctx)
		switch {
		case ctx.Err() != nil:
		case err != nil:
			// Logged once per kind of failure, not at every tick.
			if msg := err.Error(); msg != c.scanErr {
				c.scanErr = msg
				c.log.Warn("consume folder: scan stopped, the files stay for the next scan", "err", err)
			}
		case c.scanErr != "":
			c.scanErr = ""
			c.log.Info("consume folder: scans work again")
		}
		select {
		case <-ctx.Done():
			return
		case <-tick:
		}
	}
}

// Stats counts what one scan did.
type Stats struct {
	Imported, Duplicates, Failed int
	// Waiting are the files seen that are not yet unchanged for Settle.
	Waiting int
}

// Scan scans the consume folder once: it creates the missing user
// folders and imports, deletes or moves the files that are ready. An
// error is transient (the users could not be listed, or an import failed
// for a reason that is not the file's); the scan stops there and leaves
// the remaining files for the next one.
func (c *Consumer) Scan(ctx context.Context) (Stats, error) {
	var st Stats
	users, err := c.users.ListUsers(ctx)
	if err != nil {
		return st, fmt.Errorf("list users: %w", err)
	}
	root, err := os.OpenRoot(c.dir)
	if err != nil {
		c.rootProblem(err)
		return st, nil
	}
	defer root.Close()
	rootInfo, err := root.Stat(".")
	if err != nil {
		c.rootProblem(err)
		return st, nil
	}
	names, err := readDirNames(root)
	if err != nil {
		c.rootProblem(err)
		return st, nil
	}
	if c.rootErr != "" {
		c.log.Info("consume folder: readable again", "dir", c.dir)
		c.rootErr = ""
	}

	c.scanNo++
	clear(c.visited)
	clear(c.partial)

	byName := make(map[string]*store.User, len(users))
	for _, u := range users {
		if !folderName(u.Name) {
			c.warnOnce("name:"+u.Name, "consume folder: the user name cannot be a folder name; the user gets no consume folder",
				"user", u.Name)
			continue
		}
		byName[u.Name] = u
	}
	present := make(map[string]bool, len(names))
	for _, name := range names {
		present[name] = true
		if hidden(name) || byName[name] != nil {
			continue
		}
		c.unknownEntry(root, name)
	}

	userNames := make([]string, 0, len(byName))
	for name := range byName {
		userNames = append(userNames, name)
	}
	slices.Sort(userNames)
	for _, name := range userNames {
		u := byName[name]
		if !present[name] {
			c.createFolder(root, name, rootInfo.Mode().Perm())
			continue
		}
		if u.LockedAt != nil {
			c.warnOnce("locked:"+name, "consume folder: the user is locked; their folder is skipped", "user", name)
			continue
		}
		if err := c.scanUser(ctx, root, u, &st); err != nil {
			return st, err
		}
	}

	// Only a complete scan forgets, and only what it looked at.
	for k := range c.seen {
		if !c.visited[k] && !c.partial[k.user] {
			delete(c.seen, k)
		}
	}
	for k, n := range c.warned {
		if n < c.scanNo {
			delete(c.warned, k)
		}
	}
	return st, nil
}

// rootProblem logs a consume folder that cannot be read, once until it
// changes.
func (c *Consumer) rootProblem(err error) {
	if msg := err.Error(); msg != c.rootErr {
		c.rootErr = msg
		c.log.Warn("consume folder: cannot read it; retrying at every scan", "dir", c.dir, "err", err)
	}
}

// warnOnce logs a problem the first time a scan finds it and keeps quiet
// while the following scans find it again.
func (c *Consumer) warnOnce(key, msg string, args ...any) {
	if _, ok := c.warned[key]; !ok {
		c.log.Warn(msg, args...)
	}
	c.warned[key] = c.scanNo
}

// unknownEntry logs an entry of the consume folder that is no user's
// folder. It is left alone.
func (c *Consumer) unknownEntry(root *os.Root, name string) {
	fi, err := root.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err == nil && fi.IsDir():
		c.warnOnce("unknown:"+name, "consume folder: no user has this name; the folder is left alone (names match exactly)",
			"folder", name)
	default:
		c.warnOnce("loose:"+name, "consume folder: not in a user's folder; left alone (put files into consume/<user>/)",
			"name", name)
	}
}

// createFolder creates a user's folder with the consume folder's
// permissions.
func (c *Consumer) createFolder(root *os.Root, name string, perm fs.FileMode) {
	err := root.Mkdir(name, perm)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Listed as missing but there: another entry with this name
			// in another case (a case-insensitive file system), or one
			// that has just been created.
			c.warnOnce("create:"+name, "consume folder: cannot create the user's folder, the name is taken (by a name in another case?)",
				"user", name)
			return
		}
		c.warnOnce("create:"+name, "consume folder: cannot create the user's folder", "user", name, "err", err)
		return
	}
	// Mkdir is subject to the umask; the folder gets exactly the consume
	// folder's permissions where the file system supports it.
	_ = root.Chmod(name, perm)
	c.log.Info("consume folder: created the user's folder", "user", name)
}

// scanUser handles the files in one user's folder.
func (c *Consumer) scanUser(ctx context.Context, root *os.Root, u *store.User, st *Stats) error {
	lst, err := root.Lstat(u.Name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		c.warnOnce("folder:"+u.Name, "consume folder: cannot read the user's folder", "user", u.Name, "err", err)
		return nil
	case lst.Mode()&fs.ModeSymlink != 0:
		c.warnOnce("folder:"+u.Name, "consume folder: the user's folder is a symbolic link; skipped and left in place",
			"user", u.Name)
		return nil
	case !lst.IsDir():
		c.warnOnce("folder:"+u.Name, "consume folder: the user's entry is not a folder; skipped", "user", u.Name)
		return nil
	}
	ur, err := c.openRoot(root, u.Name)
	if err != nil {
		c.warnOnce("folder:"+u.Name, "consume folder: cannot open the user's folder", "user", u.Name, "err", err)
		return nil
	}
	defer ur.Close()
	// The folder that was opened must be the one that was checked: a
	// symlink put there in between is not followed.
	if fi, err := ur.Stat("."); err != nil || !os.SameFile(fi, lst) {
		return nil
	}
	names, err := readDirNames(ur)
	if err != nil {
		c.warnOnce("folder:"+u.Name, "consume folder: cannot list the user's folder", "user", u.Name, "err", err)
		return nil
	}
	for _, name := range names {
		if ignored(name) {
			continue
		}
		key := fileKey{u.ID, name}
		fi, err := ur.Lstat(name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			c.warnOnce(c.fileWarnKey("stat", key), "consume folder: cannot read the file; left in place",
				"user", u.Name, "file", name, "err", err)
			continue
		case fi.IsDir():
			continue // deeper folders are not consumed
		case fi.Mode()&fs.ModeSymlink != 0:
			c.warnOnce(c.fileWarnKey("link", key), "consume folder: a symbolic link; skipped and left in place",
				"user", u.Name, "file", name)
			continue
		case !fi.Mode().IsRegular():
			c.warnOnce(c.fileWarnKey("special", key), "consume folder: not a regular file; skipped and left in place",
				"user", u.Name, "file", name)
			continue
		}
		c.visited[key] = true
		if o, ok := c.seen[key]; ok && o.held && sameVersion(o.info, fi) && c.now().Sub(o.at) < HoldTime {
			continue
		}
		if !c.ready(key, fi) {
			st.Waiting++
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		err = c.consume(ctx, ur, u, key, fi, st)
		if errors.Is(err, store.ErrUnknownUser) {
			// Removed while this scan ran: the folder is now unknown.
			c.log.Info("consume folder: the user no longer exists; their files stay", "user", u.Name)
			c.partial[u.ID] = true
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s/%s: %w", u.Name, name, err)
		}
	}
	return nil
}

func (c *Consumer) fileWarnKey(kind string, k fileKey) string {
	return kind + ":" + strconv.FormatInt(k.user, 10) + "/" + k.name
}

// ready records the file's state and reports whether it is unchanged for
// at least Settle: seen with the same identity, size and modification
// time by an earlier scan at least Settle ago.
func (c *Consumer) ready(key fileKey, fi fs.FileInfo) bool {
	now := c.now()
	o, ok := c.seen[key]
	if !ok || !sameVersion(o.info, fi) {
		c.seen[key] = observation{info: fi, at: now}
		return false
	}
	return now.Sub(o.at) >= c.settle
}

// hold keeps a file that was read but could not be deleted or moved from
// being read again before HoldTime, unless it changes.
func (c *Consumer) hold(key fileKey, read fs.FileInfo) {
	c.seen[key] = observation{info: read, at: c.now(), held: true}
}

// sameVersion reports whether a and b describe the same file with the
// same size and modification time.
func sameVersion(a, b fs.FileInfo) bool {
	return a.Mode().IsRegular() && b.Mode().IsRegular() && os.SameFile(a, b) &&
		a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// readErrReader remembers an error from reading the file itself, to tell
// it from an error of the database or the blob store.
type readErrReader struct {
	r   io.Reader
	err error
}

func (r *readErrReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		r.err = err
	}
	return n, err
}

// consume imports one ready file and then deletes it or moves it to
// failed/. A returned error is transient and leaves the file in place.
func (c *Consumer) consume(ctx context.Context, ur *os.Root, u *store.User, key fileKey, lst fs.FileInfo, st *Stats) error {
	name := key.name
	f, err := c.open(ur, name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			delete(c.seen, key)
			return nil
		}
		c.warnOnce(c.fileWarnKey("open", key), "consume folder: cannot open the file; left in place",
			"user", u.Name, "file", name, "err", err)
		return nil
	}
	fi, err := f.Stat()
	if err != nil || !sameVersion(fi, lst) {
		// Replaced or changed since it was checked: start over.
		_ = f.Close()
		delete(c.seen, key)
		return nil //nolint:nilerr // the file's own problem, not a transient error of the scan
	}
	ok, err := complete(f, fi.Size())
	if err != nil {
		_ = f.Close()
		c.readError(u, key, fi, err)
		return nil
	}
	if !ok && c.now().Sub(c.seen[key].at) < IncompleteTime {
		// Probably still being written by a writer that paused: wait.
		_ = f.Close()
		st.Waiting++
		c.log.Debug("consume folder: the file has no end marker yet; waiting", "user", u.Name, "file", name)
		return nil
	}
	body := &readErrReader{r: f}
	res, err := c.im.Import(ctx, ingest.Request{OwnerID: u.ID, FileName: name, Body: body})
	_ = f.Close()
	if err != nil {
		if body.err != nil {
			// The file could not be read; the others may well be.
			c.readError(u, key, fi, body.err)
			return nil //nolint:nilerr // the file's own problem, not a transient error of the scan
		}
		return err
	}
	switch res.Outcome {
	case ingest.Imported:
		st.Imported++
		c.log.Info("consume folder: imported", "user", u.Name, "file", name, "document", res.Document.ID)
		c.remove(ur, u, key, fi)
	case ingest.Duplicate:
		if res.Existing.ID == "" {
			// The colliding document is gone: nothing holds this file
			// now. The next scan imports it as a new document.
			c.log.Info("consume folder: the duplicate's document no longer exists; left for the next scan",
				"user", u.Name, "file", name)
			return nil
		}
		st.Duplicates++
		c.log.Info("consume folder: duplicate, the user already has this file", "user", u.Name, "file", name,
			"document", res.Existing.ID)
		c.remove(ur, u, key, fi)
	case ingest.Refused:
		if c.moveToFailed(ur, u, key, fi, res.Refusal) {
			st.Failed++
		}
	default:
		return fmt.Errorf("unexpected import outcome %v", res.Outcome)
	}
	return nil
}

// readError holds a file that could not be read: it is read again after
// HoldTime or when it changes, not at every scan.
func (c *Consumer) readError(u *store.User, key fileKey, fi fs.FileInfo, err error) {
	c.warnOnce(c.fileWarnKey("read", key), "consume folder: cannot read the file; left in place, trying again in an hour",
		"user", u.Name, "file", key.name, "err", err)
	c.hold(key, fi)
}

// Kinds of file the completeness check knows.
var (
	pdfEnd  = []byte("%%EOF")
	jpegEnd = []byte{0xFF, 0xD9}
	pngEnd  = []byte{'I', 'E', 'N', 'D', 0xAE, 0x42, 0x60, 0x82}
	pngSig  = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}
)

// Where the end markers must be: %%EOF within the last 1 KiB of a PDF,
// the JPEG end of image and the PNG IEND chunk within the last bytes
// (a few bytes of padding after them are allowed).
const (
	pdfTail   = 1024
	imageTail = 32
)

// complete reports whether a PDF, JPEG or PNG has its end marker, so a
// file whose writer paused is not taken for a finished one. Other types
// (HEIC/HEIF has no reliable marker, unsupported files are refused
// anyway) are always complete.
func complete(f io.ReaderAt, size int64) (bool, error) {
	head := make([]byte, min(size, 1024))
	if _, err := f.ReadAt(head, 0); err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	var marker []byte
	tail := int64(imageTail)
	switch {
	case bytes.Contains(head, []byte("%PDF-")):
		marker, tail = pdfEnd, pdfTail
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		marker = jpegEnd
	case bytes.HasPrefix(head, pngSig):
		marker = pngEnd
	default:
		return true, nil
	}
	tail = min(tail, size)
	end := make([]byte, tail)
	if _, err := f.ReadAt(end, size-tail); err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return bytes.Contains(end, marker), nil
}

// remove deletes a consumed file if it is still the version that was read.
func (c *Consumer) remove(ur *os.Root, u *store.User, key fileKey, read fs.FileInfo) {
	delete(c.seen, key)
	cur, err := ur.Lstat(key.name)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil || !sameVersion(cur, read) {
		// Its next version is imported once it settles.
		c.log.Info("consume folder: the file changed while it was imported; left in place",
			"user", u.Name, "file", key.name)
		return
	}
	if err := ur.Remove(key.name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// Tried again as a duplicate after HoldTime.
		c.warnOnce(c.fileWarnKey("remove", key), "consume folder: cannot delete the consumed file; trying again in an hour",
			"user", u.Name, "file", key.name, "err", err)
		c.hold(key, read)
	}
}

// moveToFailed moves a refused file to failed/ under a free name and
// writes its sidecar. It reports whether the file was moved.
func (c *Consumer) moveToFailed(ur *os.Root, u *store.User, key fileKey, read fs.FileInfo, why *ingest.RefusedError) bool {
	name := key.name
	delete(c.seen, key)
	if !c.failedDir(ur, u) {
		c.hold(key, read)
		return false
	}
	cur, err := ur.Lstat(name)
	if err != nil || !sameVersion(cur, read) {
		// Changed since it was read: the new version gets its own check.
		return false
	}
	target, ok := freeName(ur, name)
	if !ok {
		c.warnOnce(c.fileWarnKey("move", key), "consume folder: no free name in failed/; the refused file is left in place",
			"user", u.Name, "file", name)
		c.hold(key, read)
		return false
	}
	// No other writer is expected in failed/; the check above and the
	// rename are not atomic.
	if err := ur.Rename(name, path.Join(FailedDir, target)); err != nil {
		c.warnOnce(c.fileWarnKey("move", key), "consume folder: cannot move the refused file to failed/; left in place",
			"user", u.Name, "file", name, "err", err)
		c.hold(key, read)
		return false
	}
	reason := ReasonText(why)
	c.log.Info("consume folder: refused, moved to failed/", "user", u.Name, "file", name, "as", target, "reason", reason)
	sidecar := path.Join(FailedDir, target+ErrorSuffix)
	text := fmt.Sprintf("%s: %s\n\nMoved here by paperless-go at %s. Put a fixed file into the folder %s/ to try again.\n",
		name, reason, c.now().UTC().Format(time.RFC3339), u.Name)
	if err := writeNew(ur, sidecar, text); err != nil {
		c.log.Warn("consume folder: cannot write the error file", "user", u.Name, "file", sidecar, "err", err)
	}
	return true
}

// failedDir makes sure failed/ is a real folder in the user's folder.
func (c *Consumer) failedDir(ur *os.Root, u *store.User) bool {
	fi, err := ur.Lstat(FailedDir)
	if errors.Is(err, fs.ErrNotExist) {
		perm := fs.FileMode(0o750)
		if parent, err := ur.Stat("."); err == nil {
			perm = parent.Mode().Perm()
		}
		if err := ur.Mkdir(FailedDir, perm); err != nil && !errors.Is(err, fs.ErrExist) {
			c.warnOnce("failed:"+u.Name, "consume folder: cannot create failed/; refused files are left in place",
				"user", u.Name, "err", err)
			return false
		}
		_ = ur.Chmod(FailedDir, perm)
		fi, err = ur.Lstat(FailedDir)
	}
	if err != nil || !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
		c.warnOnce("failed:"+u.Name, "consume folder: failed/ is not a folder; refused files are left in place",
			"user", u.Name)
		return false
	}
	return true
}

// freeName finds a name in failed/ that neither a file nor a sidecar
// uses: the name itself, then name-1.ext, name-2.ext and so on.
func freeName(ur *os.Root, name string) (string, bool) {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if len(ext) > maxNameBytes/2 {
		stem, ext = name, ""
	}
	for n := 0; n <= maxSuffix; n++ {
		suffix := ""
		if n > 0 {
			suffix = "-" + strconv.Itoa(n)
		}
		// The sidecar's name must fit too: shorten the stem, keep the
		// extension.
		target := cutBytes(stem, maxNameBytes-len(ErrorSuffix)-len(suffix)-len(ext)) + suffix + ext
		if !exists(ur, path.Join(FailedDir, target)) && !exists(ur, path.Join(FailedDir, target+ErrorSuffix)) {
			return target, true
		}
	}
	return "", false
}

// cutBytes cuts s to at most n bytes on a character boundary.
func cutBytes(s string, n int) string {
	for len(s) > n {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

func exists(r *os.Root, name string) bool {
	_, err := r.Lstat(name)
	return !errors.Is(err, fs.ErrNotExist)
}

func writeNew(r *os.Root, name, text string) error {
	f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, text); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ReasonText says in plain words why a file was refused.
func ReasonText(r *ingest.RefusedError) string {
	if r == nil {
		return "refused"
	}
	var s string
	switch r.Reason {
	case ingest.ReasonUnsupported:
		s = "unsupported file type (PDF, JPEG, PNG and HEIC/HEIF are accepted)"
	case ingest.ReasonTooLarge:
		s = "file too large"
	case ingest.ReasonTooManyPixels:
		s = "image has too many pixels"
	case ingest.ReasonUnreadable:
		s = "file cannot be read as its type"
	default:
		s = string(r.Reason)
	}
	if d := strings.TrimSpace(r.Detail); d != "" {
		s += " (" + d + ")"
	}
	return s
}

func readDirNames(r *os.Root) ([]string, error) {
	d, err := r.Open(".")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	return names, nil
}

// folderName reports whether a user name can be a folder name: one path
// component, not "." or "..", and not hidden.
func folderName(name string) bool {
	return name != "" && !hidden(name) && !strings.ContainsAny(name, "/\\\x00")
}

func hidden(name string) bool { return strings.HasPrefix(name, ".") }

// ignored reports whether a name in a user's folder is never consumed:
// hidden files, partial downloads and temporary files, Office lock files
// and failed/.
func ignored(name string) bool {
	lower := strings.ToLower(name)
	return hidden(name) || name == FailedDir || strings.HasPrefix(name, "~$") ||
		strings.HasSuffix(lower, ".part") || strings.HasSuffix(lower, ".tmp")
}
