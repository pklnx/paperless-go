package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/crypto"
	"github.com/pklnx/paperless-go/internal/store/db"
)

// Two-factor authentication (TOTP, RFC 6238), ported from mail-archive. The
// secret is sealed with PAPERLESS_GO_SECRET_KEY and bound to the user ID;
// recovery codes are stored as keyed hashes. Every change to a user's 2FA
// state increments users.two_factor_version, which ends pending login
// challenges: a reset always wins against a login that is in progress.
// Every time comes from the caller (the server clock), so tests move a
// clock instead of waiting.

// Errors of the second factor. Callers answer all of them alike, so that a
// response does not tell which part was wrong.
var (
	ErrTwoFactorInvalid = errors.New("store: invalid two-factor code")
	ErrTwoFactorExpired = errors.New("store: two-factor challenge expired or invalid")
)

// ErrConflict means the 2FA state does not allow the change: setup while
// 2FA is on, or turning it off for an admin.
var ErrConflict = errors.New("store: conflicting two-factor state")

// The second factor that was used, as recorded in the audit log.
const (
	FactorTOTP         = "totp"
	FactorRecoveryCode = "recovery_code"
)

// TwoFactorState is a user's 2FA setup.
type TwoFactorState struct {
	Enabled      bool
	SetupPending bool
	IsAdmin      bool
	// RecoveryCodesLeft counts the unused recovery codes.
	RecoveryCodesLeft int64
}

// secretContext binds a sealed secret to its user: a secret copied to
// another user's row does not open.
func secretContext(userID int64) []byte {
	return []byte("totp-secret:user:" + strconv.FormatInt(userID, 10))
}

func pendingSecretContext(userID int64) []byte {
	return []byte("totp-secret:user:" + strconv.FormatInt(userID, 10) + ":pending")
}

// GetTwoFactorState returns a user's 2FA state, without secrets.
func (s *Store) GetTwoFactorState(ctx context.Context, id int64) (*TwoFactorState, error) {
	r, err := s.q.GetUserByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	left, err := s.q.CountUnusedRecoveryCodes(ctx, id)
	if err != nil {
		return nil, err
	}
	return &TwoFactorState{
		Enabled: r.TwoFactorEnabled, SetupPending: len(r.TwoFactorPendingSecret) > 0,
		IsAdmin: r.IsAdmin, RecoveryCodesLeft: left,
	}, nil
}

func lockUserRow(ctx context.Context, q *db.Queries, id int64) (db.User, error) {
	u, err := q.LockUserRow(ctx, id)
	return u, mapErr(err)
}

// BeginTwoFactorSetup stores a new pending secret, replacing an earlier
// pending one. It fails with ErrConflict if 2FA is already on.
func (s *Store) BeginTwoFactorSetup(ctx context.Context, id int64, secret string, sealer *crypto.Sealer) error {
	enc, err := sealer.Seal([]byte(secret), pendingSecretContext(id))
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockUserRow(ctx, q, id)
		if err != nil {
			return err
		}
		if u.TwoFactorEnabled {
			return ErrConflict
		}
		return q.BeginTwoFactorSetup(ctx, db.BeginTwoFactorSetupParams{ID: id, TwoFactorPendingSecret: enc})
	})
}

// ConfirmTwoFactorSetup turns 2FA on if code is valid at now for the
// pending secret, and stores the recovery codes. The confirmation code
// counts as used: it cannot be used again for a login.
func (s *Store) ConfirmTwoFactorSetup(ctx context.Context, id int64, code string, now time.Time, sealer *crypto.Sealer, recovery []string, key []byte) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockUserRow(ctx, q, id)
		if err != nil {
			return err
		}
		if u.LockedAt != nil || u.TwoFactorEnabled || len(u.TwoFactorPendingSecret) == 0 {
			return ErrTwoFactorInvalid
		}
		secret, err := sealer.Open(u.TwoFactorPendingSecret, pendingSecretContext(id))
		if err != nil {
			return err
		}
		counter, ok, err := auth.ValidateTOTP(string(secret), code, now)
		if err != nil {
			return err
		}
		if !ok {
			return ErrTwoFactorInvalid
		}
		enc, err := sealer.Seal(secret, secretContext(id))
		if err != nil {
			return err
		}
		last := int64(counter) //nolint:gosec // time steps fit easily
		if err := q.EnableTwoFactor(ctx, db.EnableTwoFactorParams{ID: id, TwoFactorSecret: enc, TwoFactorLastCounter: &last}); err != nil {
			return err
		}
		if err := replaceRecoveryCodes(ctx, q, id, recovery, key); err != nil {
			return err
		}
		return q.DeleteUserTwoFactorChallenges(ctx, id)
	})
}

func replaceRecoveryCodes(ctx context.Context, q *db.Queries, id int64, codes []string, key []byte) error {
	if err := q.DeleteRecoveryCodes(ctx, id); err != nil {
		return err
	}
	for _, code := range codes {
		if err := q.InsertRecoveryCode(ctx, db.InsertRecoveryCodeParams{UserID: id, CodeHash: auth.RecoveryCodeHash(key, code)}); err != nil {
			return err
		}
	}
	return nil
}

// checkSecondFactor accepts a TOTP code newer than the last accepted one, or
// an unused recovery code, and records its use. u must be locked. The
// pending audit entry gets the factor used and, for a recovery code, how
// many are left.
func checkSecondFactor(ctx context.Context, q *db.Queries, u db.User, code string, now time.Time, sealer *crypto.Sealer, key []byte) error {
	if u.LockedAt != nil || !u.TwoFactorEnabled || len(u.TwoFactorSecret) == 0 {
		return ErrTwoFactorInvalid
	}
	secret, err := sealer.Open(u.TwoFactorSecret, secretContext(u.ID))
	if err != nil {
		return err
	}
	counter, ok, err := auth.ValidateTOTP(string(secret), code, now)
	if err != nil {
		return err
	}
	if ok {
		// Replay protection: each time step is accepted once, and never an
		// older one than the last accepted.
		last := int64(counter) //nolint:gosec // time steps fit easily
		n, err := q.AcceptTwoFactorCounter(ctx, db.AcceptTwoFactorCounterParams{ID: u.ID, TwoFactorLastCounter: &last})
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrTwoFactorInvalid
		}
		if e := auditFrom(ctx); e != nil {
			e.Detail("factor", FactorTOTP)
		}
		return nil
	}
	n, err := q.ConsumeRecoveryCode(ctx, db.ConsumeRecoveryCodeParams{Now: now, UserID: u.ID, CodeHash: auth.RecoveryCodeHash(key, code)})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrTwoFactorInvalid
	}
	if e := auditFrom(ctx); e != nil {
		left, err := q.CountUnusedRecoveryCodes(ctx, u.ID)
		if err != nil {
			return err
		}
		e.Detail("factor", FactorRecoveryCode)
		e.Detail("recoveryCodesLeft", left)
	}
	return nil
}

// VerifyTwoFactorCode checks a TOTP or recovery code of a user with 2FA on
// at now and records its use, as at a login: a code confirms one action
// only. A wrong code gives ErrTwoFactorInvalid.
func (s *Store) VerifyTwoFactorCode(ctx context.Context, id int64, code string, now time.Time, sealer *crypto.Sealer, key []byte) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockUserRow(ctx, q, id)
		if err != nil {
			return err
		}
		return checkSecondFactor(ctx, q, u, code, now, sealer, key)
	})
}

// ResetTwoFactor turns 2FA off for a user (by an admin or the CLI), removes
// the recovery codes and ends all their sessions and pending logins. It also
// works for the last admin: a lost authenticator must never lock everyone
// out. An admin without 2FA sets it up again at the next login.
func (s *Store) ResetTwoFactor(ctx context.Context, id int64) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		if _, err := lockUserRow(ctx, q, id); err != nil {
			return err
		}
		if err := disableTwoFactor(ctx, q, id); err != nil {
			return err
		}
		return q.DeleteUserSessions(ctx, id)
	})
}

func disableTwoFactor(ctx context.Context, q *db.Queries, id int64) error {
	if err := q.DisableTwoFactor(ctx, id); err != nil {
		return err
	}
	if err := q.DeleteRecoveryCodes(ctx, id); err != nil {
		return err
	}
	return q.DeleteUserTwoFactorChallenges(ctx, id)
}

// DisableOwnTwoFactor turns 2FA off at a user's own request, after checking
// their second factor in the same transaction (a wrong code gives
// ErrTwoFactorInvalid). A user for whom required(isAdmin) holds, as read
// under the row lock, cannot turn it off: ErrConflict. All other sessions end;
// keepSession (the hash of the session that asked) stays.
func (s *Store) DisableOwnTwoFactor(ctx context.Context, id int64, code string, now time.Time, sealer *crypto.Sealer, key []byte, required func(admin bool) bool, keepSession []byte) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockUserRow(ctx, q, id)
		if err != nil {
			return err
		}
		if required(u.IsAdmin) {
			return ErrConflict
		}
		if err := checkSecondFactor(ctx, q, u, code, now, sealer, key); err != nil {
			return err
		}
		if err := disableTwoFactor(ctx, q, id); err != nil {
			return err
		}
		return q.DeleteOtherSessions(ctx, db.DeleteOtherSessionsParams{UserID: id, ID: keepSession})
	})
}

// ReplaceRecoveryCodes stores a new set of recovery codes after checking
// the user's second factor in the same transaction; the old codes stop
// working, and so do pending logins.
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, id int64, code string, now time.Time, sealer *crypto.Sealer, codes []string, key []byte) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockUserRow(ctx, q, id)
		if err != nil {
			return err
		}
		if err := checkSecondFactor(ctx, q, u, code, now, sealer, key); err != nil {
			return err
		}
		if err := replaceRecoveryCodes(ctx, q, id, codes, key); err != nil {
			return err
		}
		if err := q.BumpTwoFactorVersion(ctx, id); err != nil {
			return err
		}
		return q.DeleteUserTwoFactorChallenges(ctx, id)
	})
}

// CreateTwoFactorChallenge starts the second step of a login after a
// correct password. It returns the token for the client; only its hash is
// stored.
func (s *Store) CreateTwoFactorChallenge(ctx context.Context, userID, version int64, now, expires time.Time) (string, error) {
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		return "", err
	}
	err = s.q.CreateTwoFactorChallenge(ctx, db.CreateTwoFactorChallengeParams{ID: hash, UserID: userID, TwoFactorVersion: version, Now: now, ExpiresAt: expires})
	return token, err
}

// TwoFactorChallengeUser returns the user of a challenge valid at now, for
// rate limiting before the code is checked.
func (s *Store) TwoFactorChallengeUser(ctx context.Context, token string, now time.Time) (*User, error) {
	id, err := s.q.GetTwoFactorChallengeUser(ctx, db.GetTwoFactorChallengeUserParams{ID: auth.HashSessionToken(token), Now: now})
	if err != nil {
		if errors.Is(mapErr(err), ErrNotFound) {
			return nil, ErrTwoFactorExpired
		}
		return nil, err
	}
	return s.GetUserByID(ctx, id)
}

// CompleteTwoFactorLogin checks the second factor for a challenge and, in
// the same transaction, uses up the challenge, creates the session and
// writes the audit entry of ctx. The challenge fails if the user's 2FA
// state changed since it was issued.
func (s *Store) CompleteTwoFactorLogin(ctx context.Context, token, code string, now time.Time, sealer *crypto.Sealer, key []byte, sessionHash []byte, expiresAt time.Time, userAgent string) (*User, error) {
	var out *User
	err := s.inTx(ctx, func(q *db.Queries) error {
		challengeID := auth.HashSessionToken(token)
		userID, err := q.GetTwoFactorChallengeUser(ctx, db.GetTwoFactorChallengeUserParams{ID: challengeID, Now: now})
		if err != nil {
			if errors.Is(mapErr(err), ErrNotFound) {
				return ErrTwoFactorExpired
			}
			return err
		}
		// Lock order: user, then challenge (as every 2FA change locks the user
		// first).
		u, err := lockUserRow(ctx, q, userID)
		if errors.Is(err, ErrNotFound) {
			return ErrTwoFactorExpired
		}
		if err != nil {
			return err
		}
		ch, err := q.LockTwoFactorChallenge(ctx, db.LockTwoFactorChallengeParams{ID: challengeID, Now: now})
		if err != nil {
			if errors.Is(mapErr(err), ErrNotFound) {
				return ErrTwoFactorExpired
			}
			return err
		}
		if ch.UserID != u.ID || ch.TwoFactorVersion != u.TwoFactorVersion {
			return ErrTwoFactorExpired
		}
		if err := checkSecondFactor(ctx, q, u, code, now, sealer, key); err != nil {
			return err
		}
		if err := q.DeleteTwoFactorChallenge(ctx, challengeID); err != nil {
			return err
		}
		if err := q.CreateSession(ctx, db.CreateSessionParams{ID: sessionHash, UserID: u.ID, Now: now, ExpiresAt: expiresAt, UserAgent: userAgent}); err != nil {
			return err
		}
		if err := q.RecordLogin(ctx, db.RecordLoginParams{ID: u.ID, At: now}); err != nil {
			return err
		}
		out = userFromDB(u)
		if e := auditFrom(ctx); e != nil {
			e.SetUser(u.ID, u.Name)
		}
		return nil
	})
	return out, err
}

// DeleteExpiredTwoFactorChallenges removes challenges expired at now and
// returns how many.
func (s *Store) DeleteExpiredTwoFactorChallenges(ctx context.Context, now time.Time) (int64, error) {
	return s.q.DeleteExpiredTwoFactorChallenges(ctx, now)
}
