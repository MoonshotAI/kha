package tools

import "strings"

// scrubEnvNames prevents platform and worker credentials from leaking through
// shell child environments into model-visible output.
var scrubEnvNames = map[string]bool{
	"KIMI_API_KEY":      true,
	"KHA_WORK_KEY":      true,
	"KHA_WORK_SECRET":   true,
	"KHA_SESSION_TOKEN": true,
	// The value is only a path, but reading that file exposes swt-. It does not
	// match the _WORK_SECRET suffix rule, so it must be listed explicitly.
	"KHA_WORK_SECRET_FILE": true,
}

const scrubEnvSuffix = "_WORK_SECRET"

func scrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if scrubEnvNames[name] || strings.HasSuffix(name, scrubEnvSuffix) {
			continue
		}
		out = append(out, entry)
	}
	return out
}
