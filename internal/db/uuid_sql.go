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

func (uuidSerializer) Value(_ context.Context, _ *schema.Field, _ reflect.Value, fieldValue any) (any, error) {
	switch v := fieldValue.(type) {
	case uuid.UUID:
		return v.String(), nil
	case *uuid.UUID:
		// An absent id is NULL. The nil UUID's string would satisfy NOT NULL and read
		// back as a real-looking id.
		if v == nil {
			return nil, nil //nolint:nilnil // a nil driver value is SQL NULL, not a missing result
		}
		return v.String(), nil
	default:
		return nil, fmt.Errorf("unsupported uuid value type %T", fieldValue)
	}
}

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
	case []byte:
		if len(v) == 16 {
			var u uuid.UUID
			copy(u[:], v)
			return u, nil
		}
		u, err := uuid.Parse(string(v))
		if err != nil {
			return uuid.Nil(), fmt.Errorf("parse uuid: %w", err)
		}
		return u, nil
	default:
		return uuid.Nil(), fmt.Errorf("unsupported uuid scan type %T", dbValue)
	}
}
