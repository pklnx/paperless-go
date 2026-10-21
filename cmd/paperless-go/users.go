package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/store"
)

func newUserCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage who can log in to the web UI",
		Long: `Manage who can log in to the web UI.

Create the first admin with:  user add NAME --admin`,
	}
	cmd.AddCommand(
		newUserAddCmd(),
		newUserListCmd(),
		newUserSetPasswordCmd(),
		newUserResetPasswordCmd(),
		newUserAdminCmd(true),
		newUserAdminCmd(false),
		newUserLockCmd(true),
		newUserLockCmd(false),
		newUserRemoveCmd(),
	)
	return cmd
}

// terminal returns stdin as a terminal, or nil when it is none (a pipe, a
// file, a test).
func terminal(cmd *cobra.Command) *os.File {
	f, ok := cmd.InOrStdin().(*os.File)
	if ok && term.IsTerminal(int(f.Fd())) { //nolint:gosec // fd fits in int
		return f
	}
	return nil
}

// readPassword reads a password: hidden from a terminal, or one line from
// stdin with --password-stdin or when stdin is no terminal.
func readPassword(cmd *cobra.Command, fromStdin bool, prompt string) (string, error) {
	tty := terminal(cmd)
	if fromStdin || tty == nil {
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil && line == "" {
			if errors.Is(err, io.EOF) {
				return "", errors.New("no password on stdin")
			}
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		pw := strings.TrimRight(line, "\r\n")
		if pw == "" {
			return "", errors.New("empty password")
		}
		return pw, nil
	}
	fmt.Fprint(cmd.ErrOrStderr(), prompt)
	b, err := term.ReadPassword(int(tty.Fd())) //nolint:gosec // fd fits in int
	fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", errors.New("empty password")
	}
	return string(b), nil
}

// readNewPassword asks for a new web password, twice when typed in a
// terminal, and checks its length.
func readNewPassword(cmd *cobra.Command, fromStdin bool, user string) (string, error) {
	interactive := !fromStdin && terminal(cmd) != nil
	if interactive {
		fmt.Fprintf(cmd.ErrOrStderr(), "Passwords need at least %d characters.\n", auth.MinPasswordLength)
	}
	pw, err := readPassword(cmd, fromStdin, "new password for "+user+": ")
	if err != nil {
		return "", err
	}
	if err := auth.ValidatePassword(pw); err != nil {
		return "", err
	}
	if interactive {
		again, err := readPassword(cmd, false, "repeat the password: ")
		if err != nil {
			return "", err
		}
		if again != pw {
			return "", errors.New("the passwords do not match")
		}
	}
	return pw, nil
}

// hasher hashes the passwords of the user commands; tests swap it for a
// cheap one.
var hasher = auth.NewHasher(auth.DefaultParams)

func newUserAddCmd() *cobra.Command {
	var admin, passwordStdin bool
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Create a user (the password is prompted)",
		Long: `Create a user who can log in to the web UI. Names are lowercase: a-z, 0-9,
dot, hyphen and underscore. Passwords need at least 12 characters.

Admins manage users in the web UI and see every user's documents.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := auth.NormalizeUserName(args[0])
			if err := auth.ValidateUserName(name); err != nil {
				return err
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			if _, err := a.store.GetUserByName(cmd.Context(), name); err == nil {
				return fmt.Errorf("user %q already exists", name)
			}
			pw, err := readNewPassword(cmd, passwordStdin, name)
			if err != nil {
				return err
			}
			hash, err := hasher.Hash(cmd.Context(), pw)
			if err != nil {
				return err
			}
			if _, err := a.store.CreateUser(cmd.Context(), name, hash, admin); err != nil {
				if errors.Is(err, store.ErrNameTaken) {
					return fmt.Errorf("user %q already exists", name)
				}
				return err
			}
			role := "user"
			if admin {
				role = "admin"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %q created\n", role, name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&admin, "admin", false, "allow managing users and seeing every user's documents")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")
	return cmd
}

func newUserListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List users",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			users, err := a.store.ListUsers(cmd.Context())
			if err != nil {
				return err
			}
			owned, err := a.store.OwnedRowCounts(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(users) == 0 {
				fmt.Fprintf(out, "No users yet. Create the first admin with: %s user add NAME --admin\n", commandName())
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tROLE\tSTATE\tDOCUMENTS\tCREATED\tLAST LOGIN")
			// STATE says "must change password" for generated passwords.
			for _, u := range users {
				role, state, last := "user", "active", "never"
				if u.IsAdmin {
					role = "admin"
				}
				switch {
				case u.LockedAt != nil:
					state = "locked"
				case u.MustChangePassword:
					state = "must change password"
				}
				if u.LastLoginAt != nil {
					last = u.LastLoginAt.Local().Format(time.DateTime)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", u.Name, role, state, owned[u.ID].Documents, u.CreatedAt.Local().Format(time.DateOnly), last)
			}
			return w.Flush()
		},
	}
}

func loadUser(cmd *cobra.Command, name string) (*app, *store.User, error) {
	a, err := openApp(cmd.Context())
	if err != nil {
		return nil, nil, err
	}
	name = auth.NormalizeUserName(name)
	u, err := a.store.GetUserByName(cmd.Context(), name)
	if err != nil {
		a.close()
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, fmt.Errorf("user %q not found", name)
		}
		return nil, nil, err
	}
	return a, u, nil
}

func lastAdminError(err error, name string) error {
	if errors.Is(err, store.ErrLastAdmin) {
		return fmt.Errorf("%q is the last admin who can log in; create or unlock another admin first", name)
	}
	return err
}

func newUserSetPasswordCmd() *cobra.Command {
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "set-password NAME",
		Short: "Set a new password (logs the user out everywhere)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			pw, err := readNewPassword(cmd, passwordStdin, u.Name)
			if err != nil {
				return err
			}
			hash, err := hasher.Hash(cmd.Context(), pw)
			if err != nil {
				return err
			}
			if err := a.store.SetUserPassword(cmd.Context(), u.ID, hash, false); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "password of %q changed; existing logins ended\n", u.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")
	return cmd
}

func newUserResetPasswordCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset-password NAME",
		Short: "Generate a new password the user must change at the next login",
		Long: `Generate a random password and print it once. Hand it to the user: at the
next login they must choose their own password. The user is logged out
everywhere.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			pw, err := auth.GeneratePassword()
			if err != nil {
				return err
			}
			hash, err := hasher.Hash(cmd.Context(), pw)
			if err != nil {
				return err
			}
			if err := a.store.SetUserPassword(cmd.Context(), u.ID, hash, true); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "new password for %q (shown only now; to be changed at the next login):\n%s\n", u.Name, pw)
			return nil
		},
	}
}

func newUserLockCmd(lock bool) *cobra.Command {
	use, short, done := "unlock NAME", "Allow a locked user to log in again", "unlocked"
	if lock {
		use, short, done = "lock NAME", "Stop a user from logging in (logs them out everywhere)", "locked"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			if err := a.store.SetUserLocked(cmd.Context(), u.ID, lock); err != nil {
				return lastAdminError(err, u.Name)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "user %q %s\n", u.Name, done)
			return nil
		},
	}
}

func newUserAdminCmd(admin bool) *cobra.Command {
	use, alias, short, done := "demote NAME", "unadmin", "Take the admin role away", "is no longer an admin"
	if admin {
		use, alias, short, done = "promote NAME", "admin", "Make a user an admin", "is now an admin"
	}
	return &cobra.Command{
		Use:     use,
		Aliases: []string{alias},
		Short:   short,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			if err := a.store.SetUserAdmin(cmd.Context(), u.ID, admin); err != nil {
				return lastAdminError(err, u.Name)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%q %s\n", u.Name, done)
			return nil
		},
	}
}

func newUserRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME",
		Short: "Delete a user who owns no documents or metadata",
		Long: `Delete a user and their logins. A user who still owns documents, tags,
correspondents or document types cannot be removed: the command names how
many of each. Delete them first, or lock the user instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			if err := a.store.DeleteUser(cmd.Context(), u.ID); err != nil {
				var owns *store.OwnsRowsError
				if errors.As(err, &owns) {
					if owns.Counts.Total() == 0 {
						return fmt.Errorf("%q still owns data; delete it first, or lock the user with: %s user lock %s",
							u.Name, commandName(), u.Name)
					}
					return fmt.Errorf("%q still owns %s; delete them first, or lock the user with: %s user lock %s",
						u.Name, owns.Counts, commandName(), u.Name)
				}
				return lastAdminError(err, u.Name)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "user %q removed\n", u.Name)
			return nil
		},
	}
}
