package ssh

import (
	"context"
	"errors"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/db"

	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockUserRepository mocks db.Authenticator, the three methods LoadOrRegisterUser calls.
type MockUserRepository struct {
	mock.Mock
}

func (m *MockUserRepository) LoadUserByFingerprint(ctx context.Context, fingerprint string) (*db.User, *db.PublicKey, error) {
	args := m.Called(ctx, fingerprint)
	if args.Get(0) == nil && args.Get(1) == nil {
		return nil, nil, args.Error(2)
	}
	if args.Get(0) == nil {
		return nil, args.Get(1).(*db.PublicKey), args.Error(2)
	}
	if args.Get(1) == nil {
		return args.Get(0).(*db.User), nil, args.Error(2)
	}
	return args.Get(0).(*db.User), args.Get(1).(*db.PublicKey), args.Error(2)
}

func (m *MockUserRepository) RegisterUserWithKey(ctx context.Context, username, fingerprint string) (*db.User, *db.PublicKey, error) {
	args := m.Called(ctx, username, fingerprint)
	if args.Get(0) == nil && args.Get(1) == nil {
		return nil, nil, args.Error(2)
	}
	return args.Get(0).(*db.User), args.Get(1).(*db.PublicKey), args.Error(2)
}

func (m *MockUserRepository) UpdateUserActivity(ctx context.Context, user *db.User, key *db.PublicKey) error {
	return m.Called(ctx, user, key).Error(0)
}

func TestSessionFingerprint_NoPublicKey(t *testing.T) {
	t.Parallel()

	_, err := SessionFingerprint(&stubSession{})
	assert.ErrorIs(t, err, ErrNoPublicKey)
}

func TestLoadOrRegisterUser_LoadError(t *testing.T) {
	t.Parallel()
	repo := new(MockUserRepository)
	repo.On("LoadUserByFingerprint", mock.Anything, "fp").Return(nil, nil, errors.New("db error"))

	_, err := LoadOrRegisterUser(t.Context(), repo, "user", "fp", nil)
	assert.ErrorIs(t, err, ErrInternal)
}

func TestLoadOrRegisterUser_RegisterError(t *testing.T) {
	t.Parallel()
	repo := new(MockUserRepository)
	repo.On("LoadUserByFingerprint", mock.Anything, "fp").Return(nil, nil, nil)
	repo.On("RegisterUserWithKey", mock.Anything, "user", "fp").Return(nil, nil, errors.New("reg error"))

	_, err := LoadOrRegisterUser(t.Context(), repo, "user", "fp", nil)
	assert.ErrorIs(t, err, ErrRegistrationFailed)
}

// The caller here is unauthenticated, so "that name is taken" must not be told apart
// from any other refusal: it would let anyone enumerate which usernames exist. A key
// already registered is the caller's own key and tells them nothing new, so it still
// comes back as itself.
func TestLoadOrRegisterUser_MapsRegistrationFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cause    error
		want     error
		wantText string
	}{
		{
			name: "a taken username", cause: db.ErrUsernameTaken,
			want: ErrNameUnavailable, wantText: "could not register that name",
		},
		{
			name: "the caller's own key", cause: db.ErrKeyAlreadyRegistered,
			want: db.ErrKeyAlreadyRegistered, wantText: "public key already registered",
		},
		{
			name: "anything else", cause: errors.New("disk on fire"),
			want: ErrRegistrationFailed, wantText: "registration failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := new(MockUserRepository)
			repo.On("LoadUserByFingerprint", mock.Anything, "fp").Return(nil, nil, nil)
			repo.On("RegisterUserWithKey", mock.Anything, "user", "fp").Return(nil, nil, tt.cause)

			_, err := LoadOrRegisterUser(t.Context(), repo, "user", "fp", nil)

			require.ErrorIs(t, err, tt.want)
			require.ErrorContains(t, err, tt.wantText)
			if errors.Is(tt.cause, db.ErrUsernameTaken) {
				require.NotErrorIs(t, err, db.ErrUsernameTaken, "the client can tell a taken name apart")
			}
		})
	}
}

// Whether a name is allowed is a fixed rule, not a fact about other accounts, so it
// is no oracle: the player gets the reason. And it is checked before the registration
// budget is spent, or five typos lock a network out of signing up for an hour.
func TestLoadOrRegisterUser_InvalidNameGetsTheReasonAndSpendsNoBudget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		username string
		wantText string
	}{
		{name: "too long", username: "averyveryverylongname", wantText: "cannot exceed 16 characters"},
		{name: "bad characters", username: "no-dashes", wantText: "English letters, numbers, and underscores"},
		{name: "reserved prefix", username: "deleted_x", wantText: "reserved"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := new(MockUserRepository)
			repo.On("LoadUserByFingerprint", mock.Anything, "fp").Return(nil, nil, nil)

			asked := 0
			_, err := LoadOrRegisterUser(t.Context(), repo, tt.username, "fp",
				func() bool { asked++; return true })

			require.ErrorIs(t, err, db.ErrInvalidUsername)
			require.ErrorContains(t, err, tt.wantText)
			assert.Zero(t, asked, "an invalid name spent the registration budget")
			repo.AssertNotCalled(t, "RegisterUserWithKey", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// Registration is the one unauthenticated write a stranger can drive in a loop, so
// its gate has to actually refuse - and has to leave returning players alone, since
// a shared NAT would otherwise lock out a whole office once five accounts existed.
func TestLoadOrRegisterUser_RegistrationGate(t *testing.T) {
	t.Parallel()

	t.Run("a refused gate registers nobody", func(t *testing.T) {
		t.Parallel()
		repo := new(MockUserRepository)
		repo.On("LoadUserByFingerprint", mock.Anything, "fp").Return(nil, nil, nil)

		_, err := LoadOrRegisterUser(t.Context(), repo, "user", "fp",
			func() bool { return false })

		require.ErrorIs(t, err, ErrTooManyRegistrations)
		repo.AssertNotCalled(t, "RegisterUserWithKey", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("a returning player never spends the budget", func(t *testing.T) {
		t.Parallel()
		existing := &db.User{ID: testutil.UID(1), Username: "known"}
		repo := new(MockUserRepository)
		repo.On("LoadUserByFingerprint", mock.Anything, "fp").Return(existing, nil, nil)
		repo.On("UpdateUserActivity", mock.Anything, mock.Anything, mock.Anything).Return(nil)

		asked := 0
		user, err := LoadOrRegisterUser(t.Context(), repo, "known", "fp",
			func() bool { asked++; return false })

		require.NoError(t, err, "an existing account is not a registration")
		assert.Equal(t, existing, user)
		assert.Zero(t, asked, "the gate is not even consulted")
	})

	t.Run("an allowed gate registers", func(t *testing.T) {
		t.Parallel()
		fresh := &db.User{ID: testutil.UID(2), Username: "new"}
		repo := new(MockUserRepository)
		repo.On("LoadUserByFingerprint", mock.Anything, "fp").Return(nil, nil, nil)
		repo.On("RegisterUserWithKey", mock.Anything, "new", "fp").Return(fresh, &db.PublicKey{}, nil)

		user, err := LoadOrRegisterUser(t.Context(), repo, "new", "fp",
			func() bool { return true })

		require.NoError(t, err)
		assert.Equal(t, fresh, user)
	})
}
