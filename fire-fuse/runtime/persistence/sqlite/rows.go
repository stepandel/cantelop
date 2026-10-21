package sqlite

import (
	"database/sql"
	"fmt"
)

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface{ Scan(...any) error }

// rowQuerier and rowsQuerier are satisfied by both *sql.DB and *sql.Tx.
type rowQuerier interface {
	QueryRow(string, ...any) *sql.Row
}

type rowsQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
}

// queryAll decodes every row of a query in order. It always returns a non-nil
// slice; label names the rows in wrapped errors.
func queryAll[T any](
	q rowsQuerier,
	label string,
	scan func(scanner) (T, error),
	query string,
	args ...any,
) ([]T, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", label, err)
	}
	defer rows.Close()
	values := make([]T, 0)
	for rows.Next() {
		value, err := scan(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", label, err)
	}
	return values, nil
}
