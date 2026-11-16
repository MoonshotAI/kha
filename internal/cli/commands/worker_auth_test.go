package commands

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/synctest"
)

func TestWorkerLoginScopeAndLogout(t *testing.T) {
	isolateEnv(t)
	stubLoginPrompt(t, "ewk-saved")
	for _, target := range []struct{ env, url string }{{"env_1", "https://one.example/api/"}, {"env_2", "https://one.example/api"}, {"env_1", "https://two.example/api"}} {
		out, stderr, err := runCmd(t, "", "worker", "login", target.env, "--base-url", target.url)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out+stderr, "ewk-saved") {
			t.Fatal("key leaked")
		}
	}
	for _, target := range []struct {
		env, url string
		ok       bool
	}{{"env_1", "https://ONE.example/api", true}, {"env_2", "https://one.example/api", true}, {"env_1", "https://two.example/api", true}, {"env_3", "https://one.example/api", false}, {"env_1", "https://one.example/other", false}} {
		_, err := loadWorkerKey(target.url, target.env)
		if (err == nil) != target.ok {
			t.Fatalf("scope %v: %v", target, err)
		}
	}
	if _, _, err := runCmd(t, "", "worker", "logout", "env_1", "--base-url", "https://one.example/api"); err != nil {
		t.Fatal(err)
	}
	if _, err := loadWorkerKey("https://one.example/api", "env_1"); err == nil {
		t.Fatal("logout retained key")
	}
	if _, err := loadWorkerKey("https://one.example/api", "env_2"); err != nil {
		t.Fatal("logout removed another environment", err)
	}
	if _, err := loadWorkerKey("https://two.example/api", "env_1"); err != nil {
		t.Fatal("logout removed another endpoint", err)
	}
}

func TestWorkerPollCredentialSelection(t *testing.T) {
	for _, mode := range []string{"keychain", "file", "stdin", "bad_file", "wrong_type", "empty_file_flag", "stdin_false", "both"} {
		t.Run(mode, func(t *testing.T) {
			isolateEnv(t)
			stubLocalExecutor(t, &fakeToolExecutor{})
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.Header.Get("Authorization") != "Bearer ewk-selected" {
						t.Error("wrong credential")
					}
					w.WriteHeader(http.StatusUnauthorized)
				})
				fake := systemKeys.(*fakeKeychain)
				account, _ := workerKeyAccount(srv.URL, "env_1")
				fake.entries[workerKeyService+"/"+account] = "ewk-selected"
				args := []string{"worker", "poll", "env_1", "--base-url", srv.URL, "--max-idle", "100ms"}
				stdin := ""
				switch mode {
				case "file":
					fake.getErr = errors.New("locked")
					args = append(args, "--token-file", writeSecretFile(t, "ewk-selected"))
				case "stdin":
					fake.getErr = errors.New("locked")
					args = append(args, "--token-stdin")
					stdin = "ewk-selected\n"
				case "bad_file":
					args = append(args, "--token-file", t.TempDir()+"/missing")
				case "wrong_type":
					args = append(args, "--token-file", writeSecretFile(t, "swt-must-not-leak"))
				case "empty_file_flag":
					args = append(args, "--token-file=")
				case "stdin_false":
					args = append(args, "--token-stdin=false")
				case "both":
					args = append(args, "--token-file", writeSecretFile(t, "ewk-selected"), "--token-stdin")
				}
				_, _, err := runCmd(t, stdin, args...)
				valid := mode == "keychain" || mode == "file" || mode == "stdin"
				if valid && (exitCode(err) != 1 || calls != 1) {
					t.Fatalf("expected polls, got %d %v", calls, err)
				}
				if !valid && (exitCode(err) != 2 || calls != 0) {
					t.Fatalf("invalid input made requests: %d %v", calls, err)
				}
				if err != nil && strings.Contains(err.Error(), "swt-must-not-leak") {
					t.Fatal("token leaked")
				}
			})
		})
	}
}

func TestWorkerKeychainFailures(t *testing.T) {
	isolateEnv(t)
	stubLoginPrompt(t, "ewk-secret")
	fake := systemKeys.(*fakeKeychain)
	fake.setErr = errors.New("ewk-secret")
	_, stderr, err := runCmd(t, "", "worker", "login", "env_1")
	if err == nil || strings.Contains(stderr+err.Error(), "ewk-secret") {
		t.Fatal("keychain failure not sanitized")
	}
	if len(fake.entries) != 0 {
		t.Fatal("failed login persisted key")
	}
	fake.getErr = errors.New("locked")
	if _, err := loadWorkerKey(defaultBaseURL, "env_1"); err == nil || !strings.Contains(err.Error(), "--token-file") {
		t.Fatal("missing fallback instructions", err)
	}
}

func TestWorkerLoginRejectsWrongToken(t *testing.T) {
	isolateEnv(t)
	stubLoginPrompt(t, "swt-secret")
	_, _, err := runCmd(t, "", "worker", "login", "env_1")
	if exitCode(err) != 2 || len(systemKeys.(*fakeKeychain).entries) != 0 {
		t.Fatal("saved Session token as environment key", err)
	}
}

func TestWorkerRunEnvironmentMatrix(t *testing.T) {
	inputs := []struct{ flag, env, value string }{{"environment-id", "KHA_ENVIRONMENT_ID", "env_1"}, {"work-id", "KHA_WORK_ID", "work_1"}, {"session-id", "KHA_SESSION_ID", "sess_1"}, {"worker-id", "KHA_WORKER_ID", "wk-t"}, {"generation", "KHA_WORK_GENERATION", "3"}, {"token-file", "KHA_WORK_SECRET_FILE", ""}}
	for i, input := range inputs {
		for _, mode := range []string{"flag", "env", "both", "missing", "explicit_empty"} {
			t.Run(input.flag+"/"+mode, func(t *testing.T) {
				isolateEnv(t)
				stubLocalExecutor(t, &fakeToolExecutor{})
				synctest.Test(t, func(t *testing.T) {
					values := append([]struct{ flag, env, value string }{}, inputs...)
					values[5].value = writeSecretFile(t, "swt-selected")
					calls := 0
					srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
						calls++
						if r.URL.Path != "/v1/environments/env_1/work/work_1/heartbeat" || r.Header.Get("Authorization") != "Bearer swt-selected" {
							t.Error("wrong resolved identity or token")
						}
						raw := new(strings.Builder)
						_, _ = io.Copy(raw, r.Body)
						if !strings.Contains(raw.String(), `"worker_id":"wk-t"`) || !strings.Contains(raw.String(), `"generation":3`) {
							t.Error("wrong lease identity", raw.String())
						}
						w.WriteHeader(http.StatusPreconditionFailed)
						_, _ = fmt.Fprint(w, `{"error":{"type":"failed_precondition_error","message":"stale lease"}}`)
					})
					args := []string{"worker", "run", "--base-url", srv.URL}
					for j, v := range values {
						if j != i {
							args = append(args, "--"+v.flag, v.value)
							continue
						}
						switch mode {
						case "flag":
							args = append(args, "--"+v.flag, v.value)
						case "env":
							t.Setenv(v.env, v.value)
						case "both":
							t.Setenv(v.env, "invalid-env-value")
							args = append(args, "--"+v.flag, v.value)
						case "explicit_empty":
							t.Setenv(v.env, v.value)
							args = append(args, "--"+v.flag, "")
						}
					}
					_, _, err := runCmd(t, "not-json-and-must-not-be-read", args...)
					invalid := mode == "missing" || mode == "explicit_empty"
					if invalid {
						if exitCode(err) != 2 || calls != 0 {
							t.Fatalf("expected usage error, calls=%d err=%v", calls, err)
						}
					} else if calls != 1 || exitCode(err) != 1 {
						t.Fatalf("expected one activation attempt, calls=%d err=%v", calls, err)
					}
				})
			})
		}
	}
}

func TestWorkerRunRejectsInvalidGenerationEnv(t *testing.T) {
	for _, value := range []string{"abc", "0", "-1", "9223372036854775808", "0x10"} {
		t.Run(value, func(t *testing.T) {
			isolateEnv(t)
			t.Setenv("KHA_WORK_GENERATION", value)
			_, _, err := runCmd(t, "", "worker", "run", "--environment-id", "env_1", "--work-id", "work_1", "--session-id", "sess_1", "--worker-id", "wk-t")
			if exitCode(err) != 2 || !strings.Contains(err.Error(), "KHA_WORK_GENERATION") {
				t.Fatal("generation must fail locally", err)
			}
		})
	}
}

func TestWorkerRunStdinOverridesDispatchFile(t *testing.T) {
	isolateEnv(t)
	stubLocalExecutor(t, &fakeToolExecutor{})
	tokenPath := writeSecretFile(t, "ewk-wrong-file")
	t.Setenv("KHA_WORK_SECRET_FILE", tokenPath)
	synctest.Test(t, func(t *testing.T) {
		var auth string
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			auth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusPreconditionFailed)
		})
		_, _, err := runCmd(t, "swt-stdin", "worker", "run", "--environment-id", "env_1", "--work-id", "work_1", "--session-id", "sess_1", "--worker-id", "wk-t", "--generation", "3", "--token-stdin", "--base-url", srv.URL, "--max-idle", "100ms", "--poll-interval", "10ms")
		if exitCode(err) != 1 {
			t.Fatal("expected activation rejection", err)
		}
		if _, err := os.Stat(tokenPath); err != nil {
			t.Fatal("stdin override removed unused dispatch file")
		}
		if auth != "Bearer swt-stdin" {
			t.Fatal("stdin token was ignored")
		}
	})
}

func TestWorkerLoginNoninteractive(t *testing.T) {
	for _, mode := range []string{"stdin", "file", "both", "empty", "wrong_type"} {
		t.Run(mode, func(t *testing.T) {
			isolateEnv(t)
			args := []string{"worker", "login", "env_1"}
			stdin := "ewk-noninteractive\n"
			switch mode {
			case "stdin":
				args = append(args, "--token-stdin")
			case "file":
				args = append(args, "--token-file", writeSecretFile(t, "ewk-noninteractive"))
			case "both":
				args = append(args, "--token-stdin", "--token-file", writeSecretFile(t, "ewk-noninteractive"))
			case "empty":
				args = append(args, "--token-stdin")
				stdin = ""
			case "wrong_type":
				args = append(args, "--token-stdin")
				stdin = "swt-secret"
			}
			out, stderr, err := runCmd(t, stdin, args...)
			valid := mode == "stdin" || mode == "file"
			if valid {
				if err != nil {
					t.Fatal(err)
				}
				if key, err := loadWorkerKey(defaultBaseURL, "env_1"); err != nil || key != "ewk-noninteractive" {
					t.Fatal("noninteractive key not saved", err)
				}
			} else if exitCode(err) != 2 || len(systemKeys.(*fakeKeychain).entries) != 0 {
				t.Fatal("invalid login saved credential", err)
			}
			if strings.Contains(out+stderr, "ewk-noninteractive") || strings.Contains(out+stderr, "swt-secret") {
				t.Fatal("token echoed")
			}
		})
	}
}
