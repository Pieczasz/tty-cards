package db

import (
	"context"
	"fmt"
	"reflect"
	"uuid"

	"gorm.io/gorm/schema"
)

// Stdlib uuid.UUID has no database/sql Scanner/Valuer; GORM needs this to talk
// to a Postgres uuid column.
func init() {
	schema.RegisterSerializer("stduuid", uuidSerializer{})
}

type uuidSerializer struct{}

func (uuidSerializer) Scan(ctx context.Context, field *schema.Field, dst reflect.Value, dbValue any) error {
	u, err := parseUUID(dbValue)
	if err != nil {
		return err
	}
	field.ReflectValueOf(ctx, dst).Set(reflect.ValueOf(u))
	return nil
}

// Value takes only uuid.UUID. Scan always sets a uuid.UUID, so a *uuid.UUID field
// is refused on its first write instead of panicking on its first read.
func (uuidSerializer) Value(_ context.Context, _ *schema.Field, _ reflect.Value, fieldValue any) (any, error) {
	v, ok := fieldValue.(uuid.UUID)
	if !ok {
		return nil, fmt.Errorf("unsupported uuid value type %T", fieldValue)
	}
	return v.String(), nil
}

// pgx's database/sql driver hands a uuid column over as its text form, never as bytes.
func parseUUID(dbValue any) (uuid.UUID, error) {
	switch v := dbValue.(type) {
	case nil:
		return uuid.Nil(), nil
	case string:
		u, err := uuid.Parse(v)
		if err != nil {
			return uuid.Nil(), fmt.Errorf("parse uuid: %w", err)
		}
		return u, nil
	default:
		return uuid.Nil(), fmt.Errorf("unsupported uuid scan type %T", dbValue)
	}
}
