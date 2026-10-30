package db

import (
	"fmt"
	"reflect"
	"sync"
	"testing"

	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/schema"
)

// Decision #30: nothing calls AutoMigrate, so a default: tag enforces nothing and reads
// like it does. The users.id default lives in the SQL schema; BeforeCreate is the Go side.
func TestModelsCarryNoDefaultTags(t *testing.T) {
	t.Parallel()
	for _, model := range []any{&User{}, &PublicSSHKey{}, &Game{}, &Ranking{}, &Match{}, &MatchParticipant{}} {
		s, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
		require.NoError(t, err)
		for _, field := range s.Fields {
			_, tagged := field.TagSettings["DEFAULT"]
			assert.False(t, tagged, "%s.%s carries a default tag", s.Name, field.Name)
		}
	}
}

// A column that does not parse must fail the read rather than hand back the nil UUID,
// which would pass for a real account id downstream.
func TestUUIDSerializer_Scan(t *testing.T) {
	t.Parallel()
	s, err := schema.Parse(&User{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	field := s.LookUpField("id")
	require.NotNil(t, field)
	id := uuid.New()

	tests := []struct {
		name    string
		dbValue any
		want    uuid.UUID
		wantErr string
	}{
		{name: "text form", dbValue: id.String(), want: id},
		{name: "NULL", dbValue: nil, want: uuid.Nil()},
		{name: "not a uuid", dbValue: "not-a-uuid", wantErr: "parse uuid"},
		{name: "unsupported driver type", dbValue: int64(7), wantErr: "unsupported uuid scan type int64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var user User
			err := uuidSerializer{}.Scan(t.Context(), field, reflect.ValueOf(&user).Elem(), tt.dbValue)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Equal(t, uuid.Nil(), user.ID, "a failed scan wrote the field")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, user.ID)
		})
	}
}

// Only a uuid.UUID field is supported. Scan always sets a uuid.UUID, so a pointer
// field has to fail on its first write rather than panic on its first read.
func TestUUIDSerializer_Value(t *testing.T) {
	t.Parallel()
	id := uuid.New()

	got, err := uuidSerializer{}.Value(t.Context(), nil, reflect.Value{}, id)
	require.NoError(t, err)
	assert.Equal(t, id.String(), got)

	for _, other := range []any{id.String(), &id, (*uuid.UUID)(nil)} {
		_, err := uuidSerializer{}.Value(t.Context(), nil, reflect.Value{}, other)
		require.ErrorContains(t, err, fmt.Sprintf("unsupported uuid value type %T", other))
	}
}
