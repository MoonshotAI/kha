package output

import (
	"fmt"
	"io"
	"iter"

	"encoding/json/jsontext"
	json "encoding/json/v2"

	"github.com/tidwall/gjson"
)

// Response is one API model and its original table representation. ItemsPath
// identifies the array used for per-item list transforms.
type Response struct {
	Model     any
	Headers   []string
	Rows      [][]string
	ItemsPath string
	Footer    string
}

// ResponseOptions controls response transformation and rendering.
type ResponseOptions struct {
	Format    string
	Transform string
	RawOutput bool
	stream    bool
}

func (o ResponseOptions) format() string {
	if o.Format == "table" && o.Transform == "" && o.RawOutput {
		return "json"
	}
	return o.Format
}

// List is a sequence of API models and their original table representation.
type List[T any] struct {
	Headers []string
	Items   iter.Seq2[T, error]
	Row     func(T) []string
}

// PrintResponse transforms and renders one response. Without response
// modifiers, it preserves the model's original JSON, YAML, or table output.
func PrintResponse(w io.Writer, response Response, opts ResponseOptions) error {
	if opts.Transform == "" && !opts.RawOutput {
		switch opts.Format {
		case "table":
			if err := PrintTable(w, response.Headers, response.Rows); err != nil {
				return err
			}
			if response.Footer != "" {
				_, err := fmt.Fprintln(w, StripEscapes(response.Footer))
				return err
			}
			return nil
		case "yaml":
			return printYAMLResponse(w, response.Model, opts.stream)
		case "jsonl":
			return printJSONResponse(w, response.Model, true)
		default:
			return printJSONResponse(w, response.Model, opts.stream)
		}
	}

	data, err := json.Marshal(response.Model)
	if err != nil {
		return fmt.Errorf("encoding result as JSON: %w", err)
	}
	result := gjson.ParseBytes(data)
	if opts.Transform != "" {
		result, err = transformResult(result, response.ItemsPath, opts.Transform)
		if err != nil {
			return err
		}
	}
	if opts.RawOutput {
		printed, err := printRawResult(w, result)
		if err != nil {
			return err
		}
		if printed {
			return nil
		}
	}
	if opts.format() == "table" {
		return printResponseTable(w, result)
	}

	raw, err := resultJSON(result)
	if err != nil {
		return err
	}
	if opts.format() == "yaml" {
		return printYAMLResponse(w, raw, opts.stream)
	}
	if opts.format() == "jsonl" {
		return printJSONResponse(w, raw, true)
	}
	return printJSONResponse(w, raw, opts.stream)
}

func transformResult(result gjson.Result, itemsPath, path string) (gjson.Result, error) {
	multipath := len(path) > 0 && (path[0] == '{' || path[0] == '[')
	if itemsPath != "" && !multipath {
		if transformed := result.Get(path); transformed.Exists() {
			return transformed, nil
		}
	}
	if itemsPath == "" {
		if transformed := result.Get(path); transformed.Exists() {
			return transformed, nil
		}
		return result, nil
	}
	return transformItems(result, itemsPath, path)
}

func transformItems(result gjson.Result, itemsPath, path string) (gjson.Result, error) {
	items := result.Get(itemsPath)
	if !items.Exists() || !items.IsArray() {
		return result, nil
	}
	transformed := make([]jsontext.Value, 0, len(items.Array()))
	for _, item := range items.Array() {
		value := item.Get(path)
		if !value.Exists() {
			value = item
		}
		raw, err := resultJSON(value)
		if err != nil {
			return gjson.Result{}, err
		}
		transformed = append(transformed, raw)
	}
	data, err := json.Marshal(transformed)
	if err != nil {
		return gjson.Result{}, fmt.Errorf("encoding transformed items as JSON: %w", err)
	}
	return gjson.ParseBytes(data), nil
}

func resultJSON(result gjson.Result) (jsontext.Value, error) {
	if result.Raw != "" {
		return jsontext.Value(result.Raw), nil
	}
	if result.Type == gjson.String {
		data, err := json.Marshal(result.Str)
		if err != nil {
			return nil, fmt.Errorf("encoding transformed string as JSON: %w", err)
		}
		return data, nil
	}
	return jsontext.Value(result.String()), nil
}

func printRawResult(w io.Writer, result gjson.Result) (bool, error) {
	if result.Type == gjson.String {
		_, err := io.WriteString(w, StripEscapes(result.Str)+"\n")
		return true, err
	}
	if !result.IsArray() {
		return false, nil
	}
	items := result.Array()
	for _, item := range items {
		if item.Type != gjson.String {
			return false, nil
		}
	}
	for _, item := range items {
		if _, err := io.WriteString(w, StripEscapes(item.Str)+"\n"); err != nil {
			return true, err
		}
	}
	return true, nil
}

// PrintAll renders an aggregated listing. A transform buffers the items for
// collection or per-item evaluation; all other formats retain streaming output.
func PrintAll[T any](w io.Writer, opts ResponseOptions, list List[T]) error {
	if opts.Transform != "" {
		items := []T{}
		for item, err := range list.Items {
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		return PrintResponse(w, Response{Model: items, ItemsPath: "@this"}, opts)
	}

	switch opts.format() {
	case "jsonl":
		for item, err := range list.Items {
			if err != nil {
				return err
			}
			if err := printJSONResponse(w, item, true); err != nil {
				return err
			}
		}
		return nil
	case "json":
		stream := NewArrayStream(w)
		for item, err := range list.Items {
			if err != nil {
				return err
			}
			if err := stream.Item(item); err != nil {
				return err
			}
		}
		return stream.Close()
	case "yaml":
		stream := NewSeqStream(w)
		for item, err := range list.Items {
			if err != nil {
				return err
			}
			if err := stream.Item(item); err != nil {
				return err
			}
		}
		return stream.Close()
	default:
		table, err := NewStreamTable(w, list.Headers, nil)
		if err != nil {
			return err
		}
		for item, err := range list.Items {
			if err != nil {
				return err
			}
			if err := table.Row(list.Row(item)); err != nil {
				return err
			}
		}
		return nil
	}
}

func printJSONResponse(w io.Writer, v any, stream bool) error {
	if !stream {
		return PrintJSON(w, v)
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encoding result as JSON: %w", err)
	}
	raw := jsontext.Value(data)
	if err := raw.Compact(); err != nil {
		return fmt.Errorf("encoding result as compact JSON: %w", err)
	}
	_, err = fmt.Fprintf(w, "%s\n", raw)
	return err
}

func printYAMLResponse(w io.Writer, v any, stream bool) error {
	if stream {
		if _, err := fmt.Fprintln(w, "---"); err != nil {
			return err
		}
	}
	return PrintYAML(w, v)
}
