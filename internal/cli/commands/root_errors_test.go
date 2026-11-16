package commands

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/MoonshotAI/kha"
)

func TestRenderedHTTPErrorRetainsContext(t *testing.T) {
	err := fmt.Errorf("saving agent: %w", sdkHTTPError(http.StatusBadRequest, &kha.APIError{Error: kha.ErrorObject{
		Type:      "invalid_parameter_error",
		Message:   "\x1b[31minvalid value\x1b[0m",
		Param:     new("name"),
		RequestID: new("req-body"),
	}}, http.Header{"Msh-Request-Id": []string{"req-header"}}))
	for _, format := range []string{"table", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			renderError(&out, err, format)
			text := out.String()
			for _, want := range []string{"saving agent:", "HTTP 400", "invalid_parameter_error", "invalid value", "param: name", "request id: req-body"} {
				if !strings.Contains(text, want) {
					t.Errorf("output lost %q: %s", want, text)
				}
			}
			if strings.Contains(text, "\x1b") || strings.Contains(text, `\u001b`) || strings.Contains(text, "req-header") {
				t.Fatalf("output retained escape sequences or wrong request ID: %s", text)
			}
		})
	}
}

func TestAPIErrorMessagePreservesStreamError(t *testing.T) {
	err := fmt.Errorf("watching session: %w", &kha.StreamError{Details: kha.ErrorObject{
		Type: "server_error", Message: "stream ended",
	}})
	if got := apiErrorMessage(err); got != err.Error() {
		t.Fatalf("stream error changed: %q, want %q", got, err.Error())
	}
}
