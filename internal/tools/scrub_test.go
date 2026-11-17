package tools

import (
	"slices"
	"testing"
)

func TestScrubEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"HOME=/root",
		"KIMI_API_KEY=k1",
		"KHA_WORK_KEY=k3",
		"KHA_WORK_SECRET=s2",
		"KHA_SESSION_TOKEN=t2",
		"KHA_WORK_SECRET_FILE=/tmp/x", // The path is not secret, but reading the file exposes one
		"CUSTOM_WORK_SECRET=s3",
		"WORK_SECRET=keep",         // A prefix alone must not match
		"FAKE_WORK_SECRETARY=keep", // Suffix matching must be exact
		"NO_EQUALS_SIGN",           // Preserve entries without = unchanged
	}
	want := []string{
		"PATH=/usr/bin",
		"HOME=/root",
		"WORK_SECRET=keep",
		"FAKE_WORK_SECRETARY=keep",
		"NO_EQUALS_SIGN",
	}
	got := scrubEnv(in)
	if !slices.Equal(got, want) {
		t.Fatalf("expect %v, got %v", want, got)
	}
	if len(in) != 11 {
		t.Fatalf("input mutated: %v", in)
	}
}

func TestScrubEnvEmpty(t *testing.T) {
	if got := scrubEnv(nil); len(got) != 0 {
		t.Fatalf("expect empty, got %v", got)
	}
}
