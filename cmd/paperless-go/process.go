package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/config"
	"github.com/pklnx/paperless-go/internal/process"
	"github.com/pklnx/paperless-go/internal/store"
)

// newRunner makes the tool runner of the worker; tests replace it.
var newRunner = func() process.Runner { return &process.ExecRunner{} }

// newWorker sets up the processing worker for a's data directory with
// workers at once. It checks the OCR languages. close removes the work
// directory.
func newWorker(ctx context.Context, a *app, log *slog.Logger, workers int) (w *process.Worker, closeFn func(), err error) {
	blobs, err := blobstore.New(a.cfg.DataDir)
	if err != nil {
		return nil, nil, fmt.Errorf("open the document files: %w", err)
	}
	wd, err := process.OpenWorkDir(a.cfg.DataDir, log)
	if err != nil {
		return nil, nil, fmt.Errorf("create the work directory: %w", err)
	}
	runner := newRunner()
	if err := process.CheckLanguages(ctx, runner, wd.Path(), a.cfg.OCRLanguages); err != nil {
		_ = wd.Close()
		return nil, nil, fmt.Errorf("%w (%s; set %s=0 to run without processing)", err, config.EnvOCRLanguages, config.EnvOCRWorkers)
	}
	w = &process.Worker{
		Store: a.store, Blobs: blobs, WorkDir: wd,
		Pipeline: &process.Pipeline{Runner: runner, Languages: a.cfg.OCRLanguages, Jobs: ocrJobs(workers)},
		Log:      log, Workers: workers, Timeout: a.cfg.OCRTimeout,
	}
	return w, func() { _ = wd.Close() }, nil
}

// ocrJobs is how many pages each OCRmyPDF run works on at once: the
// usable CPUs (GOMAXPROCS follows the container's CPU limit) shared by the
// workers that run at the same time.
func ocrJobs(workers int) int {
	return max(1, runtime.GOMAXPROCS(0)/max(workers, 1))
}

func newProcessCmd() *cobra.Command {
	var untilEmpty bool
	cmd := &cobra.Command{
		Use:   "process",
		Short: "Process queued documents (text, page count, archive PDF)",
		Long: `Process the documents in the queue: extract their text, count their pages
and make their archive PDF. serve does this in the background (see
` + config.EnvOCRWorkers + `); this command is for running it in the
foreground, for example after an import.

Without --until-empty it runs until it is stopped, like the worker in serve.
It can run next to serve: each job goes to one of them.`,
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
			log := newLogger(a.cfg.LogLevel)
			w, closeWorker, err := newWorker(ctx, a, log, max(a.cfg.OCRWorkers, 1))
			if err != nil {
				return err
			}
			defer closeWorker()
			if !untilEmpty {
				w.Run(ctx)
				return nil
			}
			return processUntilEmpty(ctx, w, a.store, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&untilEmpty, "until-empty", false, "stop when no queued document is due")
	return cmd
}

// processUntilEmpty processes jobs one after another until none is due and
// prints what became of them.
func processUntilEmpty(ctx context.Context, w *process.Worker, st *store.Store, out io.Writer) error {
	w.Housekeeping(ctx)
	counts := map[process.Outcome]int{}
	for {
		outcome, err := w.RunOnce(ctx)
		if err != nil {
			return err
		}
		if outcome == process.NoJob || outcome == process.Released {
			break
		}
		counts[outcome]++
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintf(out, "processed: %d, to retry: %d, failed: %d, dropped: %d\n",
		counts[process.Done], counts[process.Retry], counts[process.Failed], counts[process.Dropped])
	c, err := st.JobCounts(ctx, time.Now())
	if err != nil {
		return err
	}
	if c.Waiting > 0 {
		fmt.Fprintf(out, "%d waiting for a retry; run this again later or keep serve running\n", c.Waiting)
	}
	if c.Failed > 0 {
		fmt.Fprintf(out, "%d failed; see the errors in the web UI, then retry with: %s reprocess --failed\n", c.Failed, commandName())
	}
	return nil
}

func newReprocessCmd() *cobra.Command {
	var failed, all bool
	cmd := &cobra.Command{
		Use:   "reprocess [--failed | --all | ID...]",
		Short: "Queue documents for processing again",
		Long: `Queue documents for processing again: the ones whose processing failed
(--failed), every document (--all), or the documents with these IDs.

Processing again replaces each document's extracted text, page count and
archive PDF. Text that did not come from processing, such as text imported
from the app or edited by hand, is kept. Documents that are queued or being
processed already are left alone.

serve processes them in the background, or run "process --until-empty".`,
		RunE: func(cmd *cobra.Command, args []string) error {
			sel := store.ReprocessSelection{All: all, Failed: failed, IDs: args}
			mode := "ids"
			switch {
			case all && (failed || len(args) > 0), failed && len(args) > 0:
				return errors.New("choose one of --failed, --all or document IDs")
			case all:
				mode = "all"
			case failed:
				mode = "failed"
			case len(args) == 0:
				return errors.New("name documents by ID, or use --failed or --all")
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			e := cliEntry(cmd, "document.reprocess")
			e.Detail("selection", mode)
			if len(args) == 1 {
				e.SetTarget(store.TargetDocument, args[0], "")
			} else if len(args) > 1 {
				e.Detail("documents", args)
			}
			n, err := a.store.Reprocess(withCLIAudit(cmd, e), sel)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "queued %d documents\n", n)
			return nil
		},
	}
	cmd.Flags().BoolVar(&failed, "failed", false, "the documents whose processing failed")
	cmd.Flags().BoolVar(&all, "all", false, "every document")
	return cmd
}
