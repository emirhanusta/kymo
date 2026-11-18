package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// DefaultAdminUsername is the initial admin's username unless configured.
const DefaultAdminUsername = "admin"

const generatedPasswordLen = 24

// passwordAlphabet avoids look-alike characters (0/O, 1/l/I).
const passwordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// GeneratePassword returns a random password of n characters.
func GeneratePassword(n int) (string, error) {
	out := make([]byte, n)
	max := big.NewInt(int64(len(passwordAlphabet)))
	for i := range out {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = passwordAlphabet[v.Int64()]
	}
	return string(out), nil
}

// BootstrapOptions configures the initial admin account (KYMO_ADMIN_*).
type BootstrapOptions struct {
	Username   string // default "admin"
	Email      string // optional
	Name       string // default: the username
	Password   string // empty = generate a random one (and force a change)
	MustChange bool   // only honoured with an explicit Password
}

// BootstrapAdmin creates the initial owner when the installation has no users
// and reports whether it did. With an explicit password the account is created
// as configured. Without one a random 24-character password is generated, the
// account must change it at first sign-in, and the credentials are logged once
// (INFO). When any user exists the options are ignored.
func (s *Service) BootstrapAdmin(ctx context.Context, o BootstrapOptions) (bool, error) {
	if !s.Enabled() {
		return false, nil
	}
	n, err := s.Repo.CountUsers(ctx)
	if err != nil || n > 0 {
		return false, err
	}
	if strings.TrimSpace(o.Username) == "" {
		o.Username = DefaultAdminUsername
	}
	username, err := NormalizeUsername(o.Username)
	if err != nil {
		return false, fmt.Errorf("KYMO_ADMIN_USERNAME: %w", err)
	}
	email := ""
	if strings.TrimSpace(o.Email) != "" {
		if email, err = NormalizeEmail(o.Email); err != nil {
			return false, fmt.Errorf("KYMO_ADMIN_EMAIL: %w", err)
		}
	}
	password, mustChange, generated := o.Password, o.MustChange, false
	if password == "" {
		if password, err = GeneratePassword(generatedPasswordLen); err != nil {
			return false, err
		}
		mustChange, generated = true, true
	} else if err := ValidatePassword(password); err != nil {
		return false, fmt.Errorf("KYMO_ADMIN_PASSWORD: %w", err)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return false, err
	}
	name := strings.TrimSpace(o.Name)
	if name == "" {
		name = username
	}
	u := &model.User{Username: username, Email: email, Name: name, PasswordHash: hash, Status: model.UserActive, MustChangePassword: mustChange}
	if err := s.Repo.CreateFirstOwner(ctx, u); err != nil {
		if errors.Is(err, repository.ErrUsersExist) {
			return false, nil
		}
		return false, err
	}
	via := "env"
	if generated {
		via = "generated"
	}
	s.Record(ctx, Actor{Label: "system"}, "bootstrap", "user", fmt.Sprint(u.ID), map[string]any{"username": username, "via": via, "must_change_password": mustChange})
	if generated {
		s.Log.Info(fmt.Sprintf("initial admin account created: username %q, password %q — sign in and change it", username, password))
	} else {
		s.Log.Info("initial admin account created from KYMO_ADMIN_* environment", zap.String("username", username), zap.Bool("must_change_password", mustChange))
	}
	return true, nil
}

// ResetPassword sets a new local password for the user with that username (or,
// when username is empty, email) and revokes all of the user's sessions (used
// by `kymo admin reset-password`). A reset never forces a further change: the
// operator chose the password, so the must-change flag is cleared.
func ResetPassword(ctx context.Context, repo repository.AuthRepository, username, email, password string, s *Service) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	var (
		u   *model.User
		err error
	)
	switch {
	case strings.TrimSpace(username) != "":
		un, nerr := NormalizeUsername(username)
		if nerr != nil {
			return nerr
		}
		if u, err = repo.GetUserByUsername(ctx, un); errors.Is(err, repository.ErrNotFound) {
			return fmt.Errorf("no user with username %s", un)
		}
	default:
		em, nerr := NormalizeEmail(email)
		if nerr != nil {
			return nerr
		}
		if u, err = repo.GetUserByEmail(ctx, em); errors.Is(err, repository.ErrNotFound) {
			return fmt.Errorf("no user with email %s", em)
		}
	}
	if err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	no := false
	if err := repo.UpdateUser(ctx, u.ID, model.UserUpdate{PasswordHash: &hash, MustChangePassword: &no}); err != nil {
		return err
	}
	if _, err := repo.DeleteUserSessions(ctx, u.ID, ""); err != nil {
		return err
	}
	if s != nil {
		s.Record(ctx, Actor{Label: "cli"}, "user.password_reset", "user", fmt.Sprint(u.ID), map[string]any{"username": u.Username, "via": "cli"})
	}
	return nil
}
