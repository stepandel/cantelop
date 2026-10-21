// Package render writes command results as JSON documents or aligned tables.
package render

import (
	"encoding/json"
	"io"
	"text/tabwriter"
)

func WriteJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

// NewTable returns a writer that aligns tab-separated columns; Flush writes
// the table.
func NewTable(output io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
}
