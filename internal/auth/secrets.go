package auth

import (
	"fmt"
	"path/filepath"
	"strings"
)

// InSecretsDir checks that a file path given by an administrator resolves,
// symbolic links included, inside dir (ESTOPO_SECRETS_DIR): without it, an
// administrator of the application could make the process read any file it
// has access to and send it elsewhere. An empty dir (the variable is not
// set) or an empty path accepts everything.
func InSecretsDir(dir, path string) error {
	if dir == "" || path == "" {
		return nil
	}
	refused := fmt.Errorf("%s: not an existing file of the secrets directory (ESTOPO_SECRETS_DIR)", path)
	root, err := resolve(dir)
	if err != nil {
		return refused
	}
	p, err := resolve(path)
	if err != nil {
		return refused
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return refused
	}
	return nil
}

func resolve(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// CheckFiles applies InSecretsDir to the files of the configuration.
func (c LDAPConfig) CheckFiles(secretsDir string) error {
	for _, p := range []string{c.CAFile, c.BindPasswordFile} {
		if err := InSecretsDir(secretsDir, p); err != nil {
			return err
		}
	}
	return nil
}
