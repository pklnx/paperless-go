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
	"github.com/pklnx/paperless-go/internal/config"
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
		newUserReset2FACmd(),
		newUserRemovePasskeysCmd(),
		newUserAPIKeysCmd(),
		newUserRevokeAPIKeysCmd(),
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
			e := cliEntry(cmd, "user.add")
			e.SetTarget(store.TargetUser, "", name)
			e.Detail("admin", admin)
			if _, err := a.store.CreateUser(withCLIAudit(cmd, e), name, hash, admin); err != nil {
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
			passkeys, err := a.store.PasskeyCounts(cmd.Context())
			if err != nil {
				return err
			}
			apiKeys, err := a.store.APIKeyCounts(cmd.Context(), time.Now())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(users) == 0 {
				fmt.Fprintf(out, "No users yet. Create the first admin with: %s user add NAME --admin\n", commandName())
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tROLE\tSTATE\t2FA\tPASSKEYS\tAPI KEYS\tDOCUMENTS\tCREATED\tLAST LOGIN")
			// STATE says "must change password" for generated passwords.
			for _, u := range users {
				role, state, last, twoFactor := "user", "active", "never", "off"
				if u.TwoFactorEnabled {
					twoFactor = "on"
				}
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
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\n", u.Name, role, state, twoFactor, passkeys[u.ID], apiKeys[u.ID], owned[u.ID].Documents, u.CreatedAt.Local().Format(time.DateOnly), last)
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
			if err := a.store.SetUserPassword(withCLIAudit(cmd, cliUserEntry(cmd, "user.password_set", u)), u.ID, hash, false); err != nil {
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
	var keepPasskeys, keepAPIKeys bool
	cmd := &cobra.Command{
		Use:   "reset-password NAME",
		Short: "Generate a new password the user must change at the next login",
		Long: `Generate a random password and print it once. Hand it to the user: at the
next login they must choose their own password. The user is logged out
everywhere.

The user's passkeys are removed and their API keys revoked too, unless
--keep-passkeys or --keep-api-keys is given: after a compromise, a new
password alone would not lock the attacker out.`,
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
			e := cliUserEntry(cmd, "user.password_reset", u)
			opts := store.PasswordReset{MustChange: true, DropPasskeys: !keepPasskeys, RevokeAPIKeys: !keepAPIKeys}
			if err := a.store.ResetUserPassword(withCLIAudit(cmd, e), u.ID, hash, opts); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !keepPasskeys {
				fmt.Fprintf(out, "%v passkey(s) of %q removed\n", e.Details["passkeysRemoved"], u.Name)
			}
			if !keepAPIKeys {
				fmt.Fprintf(out, "%v API key(s) of %q revoked\n", e.Details["apiKeysRevoked"], u.Name)
			}
			fmt.Fprintf(out, "new password for %q (shown only now; to be changed at the next login):\n%s\n", u.Name, pw)
			return nil
		},
	}
	cmd.Flags().BoolVar(&keepPasskeys, "keep-passkeys", false, "do not remove the user's passkeys")
	cmd.Flags().BoolVar(&keepAPIKeys, "keep-api-keys", false, "do not revoke the user's API keys")
	return cmd
}

func newUserAPIKeysCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "api-keys NAME",
		Short: "List a user's API keys",
		Long: `List a user's API keys: name, the last characters of the key, scopes,
when it was created, when it expires, when it was last used, and its state.
Revoked and expired keys stay listed for 30 days. Keys are created in the
web UI's profile, with the password and the second factor.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			keys, err := a.store.ListAPIKeys(cmd.Context(), u.ID)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(keys) == 0 {
				fmt.Fprintf(out, "%q has no API keys\n", u.Name)
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tKEY\tSCOPES\tCREATED\tEXPIRES\tLAST USED\tSTATE")
			now := time.Now()
			for _, k := range keys {
				expires, used := "never", "never"
				if k.ExpiresAt != nil {
					expires = k.ExpiresAt.Local().Format(time.DateOnly)
				}
				if k.LastUsedAt != nil {
					used = k.LastUsedAt.Local().Format(time.DateTime)
				}
				fmt.Fprintf(w, "%s\t%s…%s\t%s\t%s\t%s\t%s\t%s\n", k.Name, auth.APIKeyPrefix, k.Hint, strings.Join(k.Scopes, ","),
					k.CreatedAt.Local().Format(time.DateOnly), expires, used, k.State(now))
			}
			return w.Flush()
		},
	}
}

func newUserRevokeAPIKeysCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke-api-keys NAME",
		Short: "Revoke all API keys of a user",
		Long: `Revoke all API keys of a user, for a leaked key or a script that should
stop. Scripts using them get 401 from now on. The keys stay listed for 30
days.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			e := cliUserEntry(cmd, "api_key.revoke", u)
			e.Detail("reason", "cli")
			n, err := a.store.RevokeUserAPIKeys(withCLIAudit(cmd, e), u.ID, nil, time.Now())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%d API key(s) of %q revoked\n", n, u.Name)
			return nil
		},
	}
}

func newUserRemovePasskeysCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove-passkeys NAME",
		Short: "Remove all passkeys of a user and log them out everywhere",
		Long: `Remove all passkeys of a user, for a lost or stolen device. The user is
logged out everywhere and logs in with their password (and second factor)
again.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			n, err := a.store.RemovePasskeys(withCLIAudit(cmd, cliUserEntry(cmd, "user.passkeys_removed", u)), u.ID)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%d passkey(s) of %q removed; existing logins ended\n", n, u.Name)
			return nil
		},
	}
}

func newUserLockCmd(lock bool) *cobra.Command {
	use, short, done, action := "unlock NAME", "Allow a locked user to log in again", "unlocked", "user.unlock"
	if lock {
		use, short, done, action = "lock NAME", "Stop a user from logging in (logs them out everywhere)", "locked", "user.lock"
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
			if err := a.store.SetUserLocked(withCLIAudit(cmd, cliUserEntry(cmd, action, u)), u.ID, lock); err != nil {
				return lastAdminError(err, u.Name)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "user %q %s\n", u.Name, done)
			return nil
		},
	}
}

func newUserAdminCmd(admin bool) *cobra.Command {
	use, alias, short, done, action := "demote NAME", "unadmin", "Take the admin role away", "is no longer an admin", "user.demote"
	if admin {
		use, alias, short, done, action = "promote NAME", "admin", "Make a user an admin", "is now an admin", "user.promote"
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
			if err := a.store.SetUserAdmin(withCLIAudit(cmd, cliUserEntry(cmd, action, u)), u.ID, admin); err != nil {
				return lastAdminError(err, u.Name)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%q %s\n", u.Name, done)
			return nil
		},
	}
}

func newUserReset2FACmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset-2fa NAME",
		Short: "Turn off a user's two-factor login (TOTP) and log them out everywhere",
		Long: `Turn off a user's two-factor login: the TOTP secret and the recovery codes
are deleted, and the user is logged out everywhere. Use it when someone lost
their authenticator and their recovery codes. It also works for the last
admin. Admins (and everybody with ` + config.EnvRequire2FA + `=true) must set up
2FA again at their next login. Until then the password alone protects the
account, so the user should log in and set it up right away.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			if err := a.store.ResetTwoFactor(withCLIAudit(cmd, cliUserEntry(cmd, "user.2fa_reset", u)), u.ID); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "two-factor login of %q reset; existing logins ended\n", u.Name)
			return nil
		},
	}
}

func newUserRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME",
		Short: "Delete a user who owns no documents, metadata or saved views",
		Long: `Delete a user and their logins. A user who still owns documents, tags,
correspondents, document types or saved views cannot be removed: the
command names how many of each. Delete them first, or lock the user
instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, u, err := loadUser(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			if err := a.store.DeleteUser(withCLIAudit(cmd, cliUserEntry(cmd, "user.remove", u)), u.ID); err != nil {
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
