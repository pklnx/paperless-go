package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/paperless-go/internal/config"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/web"
)

type app struct {
	cfg   *config.Config
	store *store.Store
}

func openApp(ctx context.Context) (*app, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	ensureAuditPartitions(ctx, st)
	return &app{cfg: cfg, store: st}, nil
}

func (a *app) close() { a.store.Close() }

func newMigrateCmd() *cobra.Command {
	runUp := func(cmd *cobra.Command, _ []string) error {
		a, err := openApp(cmd.Context())
		if err != nil {
			return err
		}
		defer a.close()
		applied, err := a.store.Migrate(cmd.Context())
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "database schema is up to date")
		}
		for _, m := range applied {
			fmt.Fprintln(cmd.OutOrStdout(), "applied", m)
		}
		return nil
	}
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Manage database schema migrations (default: apply all pending)",
		Args:  cobra.NoArgs,
		RunE:  runUp,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "up",
			Short: "Apply all pending migrations",
			Args:  cobra.NoArgs,
			RunE:  runUp,
		},
		newMigrateDownCmd(),
		newMigrateStatusCmd(),
	)
	return cmd
}

func newMigrateDownCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Roll back the most recent migration (may delete data)",
		Long: `Roll back the most recently applied migration.

Rolling back usually drops tables or columns and the data in them. Document
files on disk are not touched. Back up the database first. Intended for
development; in production, prefer restoring a backup.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				return errors.New("refusing to roll back without --yes (this may delete data)")
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			rolledBack, err := a.store.MigrateDown(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "rolled back", rolledBack)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm that data may be deleted")
	return cmd
}

func newMigrateStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show applied and pending migrations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			statuses, err := a.store.MigrationStatus(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "MIGRATION\tSTATE\tAPPLIED AT")
			for _, st := range statuses {
				state, at := "pending", "-"
				if st.Applied {
					state, at = "applied", st.AppliedAt.Local().Format(time.DateTime)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", st.Path, state, at)
			}
			return w.Flush()
		},
	}
}

func newServeCmd() *cobra.Command {
	var listen string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the web server (UI and JSON API)",
		Long: `Run the web server: the UI and the JSON API.

Everything but the health check and the UI code needs a login; create the
first admin with "user add NAME --admin". There is no two-factor login yet,
so keep the server on 127.0.0.1 for now.

Requests are only accepted with a Host header listed in
` + config.EnvAllowedHosts + ` (default: localhost, 127.0.0.1, ::1), which blocks DNS
rebinding; state-changing requests must come from the same origin. Use HTTPS
(a reverse proxy or tailscale serve) when the server is reachable from a
network.

It also processes queued documents in the background (` + config.EnvOCRWorkers + `
at a time, default 1; 0 turns it off).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			if err := checkDataDir(a.cfg.DataDir); err != nil {
				return err
			}
			if err := checkSchema(cmd.Context(), a.store); err != nil {
				return err
			}
			log := newLogger(a.cfg.LogLevel)
			warnWithoutUsers(cmd.Context(), a.store, log)
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			var worker sync.WaitGroup
			if a.cfg.OCRWorkers > 0 {
				w, closeWorker, err := newWorker(ctx, a, log, a.cfg.OCRWorkers)
				if err != nil {
					return err
				}
				defer closeWorker()
				log.Info("processing documents", "workers", a.cfg.OCRWorkers, "languages", a.cfg.OCRLanguages)
				worker.Go(func() { w.Run(ctx) })
			} else {
				log.Warn("document processing is off", "env", config.EnvOCRWorkers)
			}
			srv := web.New(a.store, log, web.Options{AllowedHosts: a.cfg.AllowedHosts, TrustedProxies: a.cfg.TrustedProxies})
			log.Info("listening", "addr", listen, "version", version)
			err = srv.ListenAndServe(ctx, listen)
			// Running jobs are given back before the process ends.
			cancel()
			worker.Wait()
			return err
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:8080", "address to listen on")
	return cmd
}

// warnWithoutUsers logs how to create the first admin while no user
// exists: until then nobody can log in.
func warnWithoutUsers(ctx context.Context, st *store.Store, log *slog.Logger) {
	n, err := st.CountUsers(ctx)
	if err != nil {
		log.Warn("count users", "err", err)
		return
	}
	if n == 0 {
		log.Warn("no users yet; create the first admin to log in", "command", commandName()+" user add NAME --admin")
	}
}

// checkSchema refuses to serve a database with pending migrations, where the
// handlers would fail on missing tables, and one migrated by a newer
// version, whose schema this binary does not know.
func checkSchema(ctx context.Context, st *store.Store) error {
	database, known, err := st.SchemaVersions(ctx)
	if err != nil {
		return err
	}
	if database > known {
		return fmt.Errorf("the database schema (version %d) is newer than this version of paperless-go knows (%d): "+
			"run the newer version, or restore a backup made before the upgrade", database, known)
	}
	statuses, err := st.MigrationStatus(ctx)
	if err != nil {
		return err
	}
	for _, s := range statuses {
		if !s.Applied {
			return fmt.Errorf("the database schema is not up to date (%s is pending): run %s migrate", s.Path, commandName())
		}
	}
	return nil
}
