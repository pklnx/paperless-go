package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/config"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/sweep"
)

// errSweepRunning answers a sweep while another one runs (serve's sweeper
// for a moment every minute, or another ./pgo sweep).
var errSweepRunning = errors.New("another sweep is running (serve's sweeper or another sweep command); try again in a minute")

func newSweepCmd() *cobra.Command {
	var dryRun, force, bind bool
	cmd := &cobra.Command{
		Use:   "sweep [--dry-run] [--force] [--bind]",
		Short: "Remove unreferenced document files now (serve does this on its own)",
		Long: `Remove the document files that no document needs any more, now. serve
does the same on its own: every minute it removes the queued files whose
hour has passed (the files of purged documents), and once a day it walks
the data directory and queues the files no document names.

This command runs one queue pass and one walk and prints what they did.
The walk only runs in a data directory bound to this installation (the
file "installation" in it, written by migrate into a fresh data directory),
and it queues nothing when the database has no documents but the data
directory has files, or when it would queue more than 10 % of the files.
It keeps the files no document names that are newer than the newest
document change (after a restore of an older backup with a newer data
directory) and warns about them.

  --bind     bind an existing data directory to this installation (once,
             after the upgrade that added the marker); it needs documents
             in the database, and --force to replace a marker of another
             installation
  --force    override the 10 % and empty-database stops for this run and
             queue the newer files too (or, with --bind, bind anyway)
  --dry-run  only report what would be done`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			a, err := openApp(ctx)
			if err != nil {
				return err
			}
			defer a.close()
			if err := checkSchema(ctx, a.store); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			dir := a.cfg.DataDir
			if bind {
				wrote, err := sweep.Bind(ctx, a.store, dir, force, dryRun)
				switch {
				case err != nil:
					return err
				case !wrote:
					fmt.Fprintf(out, "the data directory %s is already bound to this installation\n", dir)
				case dryRun:
					fmt.Fprintf(out, "would bind the data directory %s to this installation\n", dir)
				default:
					fmt.Fprintf(out, "bound the data directory %s to this installation\n", dir)
				}
				return nil
			}
			blobs, err := blobstore.New(dir)
			if err != nil {
				return fmt.Errorf("open the document files: %w", err)
			}
			log := newLogger(a.cfg.LogLevel)
			s := &sweep.Sweeper{Store: a.store, Blobs: blobs, Log: log, Now: a.store.Now, Retention: a.store.TrashRetention}
			q, err := s.QueuePass(ctx, dryRun)
			if errors.Is(err, store.ErrSweepRunning) {
				return errSweepRunning
			}
			if errors.Is(err, sweep.ErrOtherInstallation) {
				fmt.Fprintf(out, "queue: refused: %v; no file was removed. Check %s and the data directory\n", err, config.EnvDatabaseURL)
				return &exitError{code: 1, msg: "the sweep refused the data directory"}
			}
			if err != nil {
				return err
			}
			removed := "removed"
			if dryRun {
				removed = "would remove"
			}
			fmt.Fprintf(out, "queue: %s: %d, in use again: %d, failed: %d\n", removed, q.Removed, q.InUse, q.Failed)
			w, walkErr := s.Walk(ctx, sweep.WalkOptions{DryRun: dryRun, Force: force})
			queued, temp := fmt.Sprintf("queued: %d", w.Queued), "temporary files removed"
			if dryRun {
				n := w.Candidates
				if !force {
					n -= w.Newer
				}
				queued, temp = fmt.Sprintf("would queue: %d", n), "stale temporary files"
			}
			switch {
			case errors.Is(walkErr, store.ErrSweepRunning):
				return errSweepRunning
			case errors.Is(walkErr, sweep.ErrUnbound):
				fmt.Fprintf(out, "walk: refused: the data directory %s is not bound to this installation; "+
					"check that it is this installation's, then run %s sweep --bind\n", dir, commandName())
			case sweep.Refused(walkErr):
				hint := ""
				if !errors.Is(walkErr, sweep.ErrOtherInstallation) {
					hint = "; if the database and the data directory are right, run it once with --force"
				} else {
					hint = fmt.Sprintf("; check %s and the data directory", config.EnvDatabaseURL)
				}
				if w.Newer > 0 && errors.Is(walkErr, sweep.ErrTooMany) {
					hint += fmt.Sprintf("; %d of the unreferenced files are newer than the newest document change "+
						"(an older backup restored with a newer data directory?), and --force would queue them too", w.Newer)
				}
				fmt.Fprintf(out, "walk: refused: %v (files: %d, unreferenced: %d)%s\n", walkErr, w.Blobs, w.Candidates, hint)
			case walkErr != nil:
				return walkErr
			default:
				fmt.Fprintf(out, "walk: files: %d, unreferenced: %d, %s, %s: %d, unexpected entries: %d\n",
					w.Blobs, w.Candidates, queued, temp, w.Temp, w.Unexpected)
				if w.Newer > 0 && !force {
					fmt.Fprintf(out, "walk: warning: kept %d unreferenced files that are newer than the newest document change. "+
						"If an older backup was restored, put back the data directory of the same backup; "+
						"if these files are not needed, run %s sweep --force once\n", w.Newer, commandName())
				}
			}
			if walkErr != nil || q.Failed > 0 {
				return &exitError{code: 1, msg: "the sweep did not finish"}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only report what would be done")
	cmd.Flags().BoolVar(&force, "force", false, "override the sanity stops (with --bind: bind anyway)")
	cmd.Flags().BoolVar(&bind, "bind", false, "bind the data directory to this installation")
	return cmd
}
