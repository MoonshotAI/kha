package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"
	"golang.org/x/term"
)

// credentialStore is shared by API-key profiles and environment work keys.
// Tests replace it so they never access the user's system keychain.
type credentialStore interface {
	Get(service, account string) (string, error)
	Set(service, account, token string) error
	Delete(service, account string) error
}

type systemCredentialStore struct{}

func (systemCredentialStore) Get(service, account string) (string, error) {
	return keyring.Get(service, account)
}

func (systemCredentialStore) Set(service, account, token string) error {
	return keyring.Set(service, account, token)
}

func (systemCredentialStore) Delete(service, account string) error {
	return keyring.Delete(service, account)
}

var systemKeys credentialStore = systemCredentialStore{}

// promptLoginToken reads the token without echo on a terminal. Ctrl-D (EOF)
// aborts immediately; Ctrl-C cancels the context, and the line read still
// returns on Enter (x/term keeps canonical input) — either way a partial
// token is never saved.
var promptLoginToken = func(cmd *cobra.Command, title string) (string, error) {
	f, ok := cmd.InOrStdin().(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", errUsage("login requires a terminal; use an explicit stdin or file credential option for automation")
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s: ", title)
	raw, err := term.ReadPassword(int(f.Fd()))
	_, _ = fmt.Fprintln(cmd.ErrOrStderr())
	if errors.Is(err, io.EOF) {
		return "", errInterrupted
	}
	if err != nil {
		return "", err
	}
	if cmd.Context().Err() != nil {
		return "", errInterrupted
	}
	return strings.TrimSpace(string(raw)), nil
}

const apiKeyService = "kha.api-key"

func savedAPIKey(ref string) (string, error) {
	key, err := systemKeys.Get(apiKeyService, ref)
	if err != nil || key == "" {
		return "", errors.New("could not read API key from system keychain; unlock it or run kha login again (automation can use KIMI_API_KEY)")
	}
	return key, nil
}
