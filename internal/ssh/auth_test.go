package ssh

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockUserRepository mocks db.Authenticator, the three methods LoadOrRegisterUser calls.
type MockUserRepository struct {
	mock.Mock
}

func (m *MockUserRepository) LoadUserByFingerprint(ctx context.Context, fingerprint string) (*db.User, *db.PublicSSHKey, error) {
	args := m.Called(ctx, fingerprint)
	if args.Get(0) == nil && args.Get(1) == nil {
		return nil, nil, args.Error(2)
	}
	if args.Get(0) == nil {
		return nil, args.Get(1).(*db.PublicSSHKey), args.Error(2)
	}
	if args.Get(1) == nil {
		return args.Get(0).(*db.User), nil, args.Error(2)
	}
	return args.Get(0).(*db.User), args.Get(1).(*db.PublicSSHKey), args.Error(2)
}

func (m *MockUserRepository) RegisterUserWithSSHKey(ctx context.Context, username, fingerprint string) (*db.User, *db.PublicSSHKey, error) {
	args := m.Called(ctx, username, fingerprint)
	if args.Get(0) == nil && args.Get(1) == nil {
		return nil, nil, args.Error(2)
	}
	return args.Get(0).(*db.User), args.Get(1).(*db.PublicSSHKey), args.Error(2)
}

func (m *MockUserRepository) UpdateUserActivity(ctx context.Context, user *db.User, key *db.PublicSSHKey) error {
	return m.Called(ctx, user, key).Error(0)
}

func TestSessionFingerprint_NoPublicKey(t *testing.T) {
	t.Parallel()

	_, err := SessionFingerprint(&stubSession{})
	assert.ErrorIs(t, err, errNoPublicKey)
}

func TestLoadOrRegisterUser_LoadError(t *testing.T) {
	t.Parallel()
	repo := new(MockUserRepository)
	repo.On("LoadUserByFingerprint", mock.Anything, "fp").Return(nil, nil, errors.New("db error"))

	_, err := LoadOrRegisterUser(t.Context(), repo, "user", "fp", nil)
	assert.ErrorIs(t, err, errInternal)
}

func TestLoadOrRegisterUser_RegisterError(t *testing.T) {
	t.Parallel()
	repo := new(MockUserRepository)
	repo.On("LoadUserByFingerprint", mock.Anything, "fp").Return(nil, nil, nil)
	repo.On("RegisterUserWithSSHKey", mock.Anything, "user", "fp").Return(nil, nil, errors.New("reg error"))

	_, err := LoadOrRegisterUser(t.Context(), repo, "user", "fp", nil)
	assert.ErrorIs(t, err, errRegistrationFailed)
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
			want: errNameUnavailable, wantText: "could not register that name",
		},
		{
			name: "the caller's own key", cause: db.ErrKeyAlreadyRegistered,
			want: db.ErrKeyAlreadyRegistered, wantText: "public key already registered",
		},
		{
			name: "a name the repository refuses", cause: fmt.Errorf("%w: too long", db.ErrInvalidUsername),
			want: db.ErrInvalidUsername, wantText: "too long",
		},
		{
			name: "anything else", cause: errors.New("disk on fire"),
			want: errRegistrationFailed, wantText: "registration failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := new(MockUserRepository)
			repo.On("LoadUserByFingerprint", mock.Anything, "fp").Return(nil, nil, nil)
			repo.On("RegisterUserWithSSHKey", mock.Anything, "user", "fp").Return(nil, nil, tt.cause)

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
				func() (func(), error) { asked++; return nil, nil })

			require.ErrorIs(t, err, db.ErrInvalidUsername)
			require.ErrorContains(t, err, tt.wantText)
			assert.Zero(t, asked, "an invalid name spent the registration budget")
			repo.AssertNotCalled(t, "RegisterUserWithSSHKey", mock.Anything, mock.Anything, mock.Anything)
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
			func() (func(), error) { return nil, errTooManyRegistrations })

		require.ErrorIs(t, err, errTooManyRegistrations)
		repo.AssertNotCalled(t, "RegisterUserWithSSHKey", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("a returning player never spends the budget", func(t *testing.T) {
		t.Parallel()
		existing := &db.User{ID: testutil.UID(1), Username: "known"}
		repo := new(MockUserRepository)
		repo.On("LoadUserByFingerprint", mock.Anything, "fp").Return(existing, nil, nil)
		repo.On("UpdateUserActivity", mock.Anything, mock.Anything, mock.Anything).Return(nil)

		asked := 0
		user, err := LoadOrRegisterUser(t.Context(), repo, "known", "fp",
			func() (func(), error) { asked++; return nil, errTooManyRegistrations })

		require.NoError(t, err, "an existing account is not a registration")
		assert.Equal(t, existing, user)
		assert.Zero(t, asked, "the gate is not even consulted")
	})

	t.Run("an allowed gate registers", func(t *testing.T) {
		t.Parallel()
		fresh := &db.User{ID: testutil.UID(2), Username: "new"}
		repo := new(MockUserRepository)
		repo.On("LoadUserByFingerprint", mock.Anything, "fp").Return(nil, nil, nil)
		repo.On("RegisterUserWithSSHKey", mock.Anything, "new", "fp").Return(fresh, &db.PublicSSHKey{}, nil)

		user, err := LoadOrRegisterUser(t.Context(), repo, "new", "fp",
			func() (func(), error) { return nil, nil })

		require.NoError(t, err)
		assert.Equal(t, fresh, user)
	})
}

// A stale last-seen timestamp is cosmetic; refusing a returning player over it is not.
func TestLoadOrRegisterUser_ActivityUpdateFailureDoesNotBlockLogin(t *testing.T) {
	t.Parallel()
	existing := &db.User{ID: testutil.UID(3), Username: "known"}
	repo := new(MockUserRepository)
	repo.On("LoadUserByFingerprint", mock.Anything, "fp").Return(existing, &db.PublicSSHKey{}, nil)
	repo.On("UpdateUserActivity", mock.Anything, existing, mock.Anything).Return(errors.New("deadlock detected"))

	user, err := LoadOrRegisterUser(t.Context(), repo, "known", "fp", nil)

	require.NoError(t, err)
	assert.Equal(t, existing, user)
	repo.AssertExpectations(t)
}

// A name that is taken (or a key that raced) made no account, so it must not cost the
// network one of its few registrations.
func TestLoadOrRegisterUser_AFailedRegistrationRefundsTheGate(t *testing.T) {
	t.Parallel()
	repo := new(MockUserRepository)
	repo.On("LoadUserByFingerprint", mock.Anything, "fp").Return(nil, nil, nil)
	repo.On("RegisterUserWithSSHKey", mock.Anything, "user", "fp").Return(nil, nil, db.ErrUsernameTaken)

	refunded := 0
	_, err := LoadOrRegisterUser(t.Context(), repo, "user", "fp",
		func() (func(), error) { return func() { refunded++ }, nil })

	require.ErrorIs(t, err, errNameUnavailable)
	assert.Equal(t, 1, refunded)
}

// A stalled database must not hold an unauthenticated connection open until the idle
// timeout: every round trip login makes carries a deadline.
func TestLoadOrRegisterUser_EveryRoundTripHasADeadline(t *testing.T) {
	t.Parallel()
	hasDeadline := mock.MatchedBy(func(ctx context.Context) bool {
		_, ok := ctx.Deadline()
		return ok
	})
	repo := new(MockUserRepository)
	repo.On("LoadUserByFingerprint", hasDeadline, "fp").Return(nil, nil, nil).Once()
	repo.On("RegisterUserWithSSHKey", hasDeadline, "user", "fp").
		Return(&db.User{ID: testutil.UID(4)}, &db.PublicSSHKey{}, nil).Once()

	_, err := LoadOrRegisterUser(t.Context(), repo, "user", "fp", nil)

	require.NoError(t, err)
	repo.AssertExpectations(t)
}

// A server with no room refuses a newcomer before the account exists, not after.
func TestSessionTracker_FullMeansANewcomerIsRefusedUpFront(t *testing.T) {
	t.Parallel()
	tracker := NewSessionTracker(1)
	assert.False(t, tracker.Full())
	_, err := tracker.Connect(testutil.UID(1), nil)
	require.NoError(t, err)
	assert.True(t, tracker.Full())
	assert.False(t, NewSessionTracker(0).Full(), "zero means unlimited")
}
