package db

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/schema"
)

// A nil *uuid.UUID is an absent id. Writing it as the nil UUID's string stored
// 00000000-... - a value that looks like an id and satisfies NOT NULL - instead of the
// NULL the caller meant.
func TestUUIDSerializer_NilPointerIsNull(t *testing.T) {
	t.Parallel()
	id := uuid.New()

	tests := []struct {
		name  string
		value any
		want  any
	}{
		{name: "value", value: id, want: id.String()},
		{name: "pointer", value: &id, want: id.String()},
		{name: "nil pointer", value: (*uuid.UUID)(nil), want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := uuidSerializer{}.Value(context.Background(), nil, reflect.Value{}, tt.value)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Decision #30: nothing calls AutoMigrate, so a default: tag enforces nothing and reads
// like it does. The users.id default lives in 000001; BeforeCreate is the Go side.
func TestModelsCarryNoDefaultTags(t *testing.T) {
	t.Parallel()
	for _, model := range []any{&User{}, &PublicKey{}, &Game{}, &Ranking{}, &Match{}, &MatchParticipant{}} {
		s, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
		require.NoError(t, err)
		for _, field := range s.Fields {
			_, tagged := field.TagSettings["DEFAULT"]
			assert.False(t, tagged, "%s.%s carries a default tag", s.Name, field.Name)
		}
	}
}
