package output

import (
	"fmt"
	"io"

	"encoding/json/jsontext"

	"github.com/tidwall/gjson"
)

func printResponseTable(w io.Writer, result gjson.Result) error {
	if result.IsObject() {
		return printObjectTable(w, []gjson.Result{result})
	}
	if result.IsArray() {
		items := result.Array()
		if len(items) == 0 {
			return printValueTable(w, []gjson.Result{result})
		}
		for _, item := range items {
			if !item.IsObject() {
				return printValueTable(w, items)
			}
		}
		return printObjectTable(w, items)
	}
	return printValueTable(w, []gjson.Result{result})
}

func printObjectTable(w io.Writer, objects []gjson.Result) error {
	headers := []string{}
	indexes := map[string]int{}
	for _, object := range objects {
		object.ForEach(func(key, _ gjson.Result) bool {
			name := key.String()
			if _, ok := indexes[name]; !ok {
				indexes[name] = len(headers)
				headers = append(headers, name)
			}
			return true
		})
	}
	if len(headers) == 0 {
		return printValueTable(w, objects)
	}

	rows := make([][]string, len(objects))
	for i, object := range objects {
		values := object.Map()
		row := make([]string, len(headers))
		for j, name := range headers {
			value, ok := values[name]
			if !ok {
				continue
			}
			cell, err := responseTableCell(value)
			if err != nil {
				return err
			}
			row[j] = cell
		}
		rows[i] = row
	}
	return PrintTable(w, headers, rows)
}

func printValueTable(w io.Writer, values []gjson.Result) error {
	rows := make([][]string, len(values))
	for i, value := range values {
		cell, err := responseTableCell(value)
		if err != nil {
			return err
		}
		rows[i] = []string{cell}
	}
	return PrintTable(w, []string{"VALUE"}, rows)
}

func responseTableCell(result gjson.Result) (string, error) {
	if result.Type == gjson.String {
		return result.Str, nil
	}
	if result.Type == gjson.Null {
		return "null", nil
	}
	if result.IsObject() || result.IsArray() {
		raw := jsontext.Value(result.Raw)
		if err := raw.Compact(); err != nil {
			return "", fmt.Errorf("formatting table value as JSON: %w", err)
		}
		return string(raw), nil
	}
	return result.String(), nil
}
