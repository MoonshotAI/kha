package output

import "io"

// StreamPrinter renders independent responses in NDJSON, YAML document, or
// table form. An untransformed table keeps its original fixed header.
type StreamPrinter struct {
	w     io.Writer
	opts  ResponseOptions
	table *StreamTable
}

// NewStreamPrinter creates a printer for a live response stream.
func NewStreamPrinter(w io.Writer, opts ResponseOptions, headers []string, widths []int) (*StreamPrinter, error) {
	printer := &StreamPrinter{w: w, opts: opts}
	if opts.Format == "table" && opts.Transform == "" && !opts.RawOutput {
		table, err := NewStreamTable(w, headers, widths)
		if err != nil {
			return nil, err
		}
		printer.table = table
	}
	return printer, nil
}

// Print renders one stream item. row is evaluated only for an untransformed
// table; transformed tables are derived from the model itself.
func (p *StreamPrinter) Print(v any, row func() []string) error {
	if p.table != nil {
		return p.table.Row(row())
	}
	opts := p.opts
	opts.stream = true
	return PrintResponse(p.w, Response{Model: v}, opts)
}
