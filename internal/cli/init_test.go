// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
)

func TestStarterConfigurationIsTheDefaults(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), config.FileName)
	file, err := config.Parse(path, []byte(StarterConfig()))
	if err != nil {
		t.Fatalf("the file `init` writes does not parse: %v", err)
	}
	resolved := config.Merge(config.Defaults(), file, config.Overlay{})
	if err = resolved.Validate(); err != nil {
		t.Fatalf("the file `init` writes does not validate: %v", err)
	}
	if diff := cmp.Diff(config.Defaults(), resolved); diff != "" {
		t.Errorf("the generated configuration is not the defaults (-want +got):\n%s", diff)
	}
}

func TestStarterConfigurationWritesEveryKeyTheSchemaDefines(t *testing.T) {
	t.Parallel()

	keys := config.SchemaKeys()
	if len(keys) == 0 {
		t.Fatal("the configuration schema defines no keys at all")
	}
	written := starterKeys(t, StarterConfig())
	for _, key := range keys {
		if isTableKey(key, keys) {
			if _, found := written[key]; !found {
				t.Errorf("the schema defines the table %s, and the file `init` writes has no such section", key)
			}
			continue
		}
		section, name := splitLastKey(key)
		if !written[section][name] {
			t.Errorf("%s is a setting go-mutants reads and the file `init` writes never mentions it; "+
				"write it under [%s] as `%s = ` or `# %s = `", key, section, name, name)
		}
	}
}

func isTableKey(key string, keys []string) bool {
	for _, other := range keys {
		if strings.HasPrefix(other, key+".") {
			return true
		}
	}
	return false
}

func splitLastKey(key string) (section, name string) {
	i := strings.LastIndex(key, ".")
	if i < 0 {
		return "", key
	}
	return key[:i], key[i+1:]
}

func starterKeys(t *testing.T, content string) map[string]map[string]bool {
	t.Helper()
	written := map[string]map[string]bool{"": {}}
	section := ""
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "#"))
		switch {
		case strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]"):
			section = strings.TrimSuffix(strings.TrimPrefix(line, "[["), "]]")
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
		default:
			name, _, found := strings.Cut(line, "=")
			name = strings.TrimSpace(name)
			if !found || !isBareKey(name) {
				continue
			}
			if written[section] == nil {
				written[section] = map[string]bool{}
			}
			written[section][name] = true
			continue
		}
		if written[section] == nil {
			written[section] = map[string]bool{}
		}
	}
	return written
}

func isBareKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func TestStarterConfigurationDoesNotDependOnTheMachine(t *testing.T) {
	t.Parallel()

	text := StarterConfig()
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "jobs") || strings.HasPrefix(line, "timeout") {
			t.Errorf("a derived default is written out as a value: %q", line)
		}
	}
	for _, needed := range []string{"# jobs = ", "# timeout = ", "min(CPU count, 8)"} {
		if !strings.Contains(text, needed) {
			t.Errorf("the file does not explain %q", needed)
		}
	}
	if StarterConfig() != text {
		t.Error("two calls produced different files")
	}
}

func TestInitWritesOnceAndNeverAgain(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, config.FileName)

	code, stdout, stderr := execute(t, "init")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, path) {
		t.Errorf("stdout does not name the file it wrote: %q", stdout)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the written file: %v", err)
	}
	if string(written) != StarterConfig() {
		t.Error("the file on disk is not what `init` generates")
	}

	edited := string(written) + "\n# a decision somebody made\n"
	if err = os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatalf("editing the file: %v", err)
	}
	code, _, stderr = execute(t, "init")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d for a second init, want 2", code)
	}
	if !strings.Contains(stderr, "error "+string(CodeConfigurationExists)) {
		t.Errorf("stderr = %q, want the refusal's own code", stderr)
	}
	if !strings.Contains(stderr, "hint: ") {
		t.Errorf("stderr = %q, want a hint naming the way out", stderr)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the file after the refusal: %v", err)
	}
	if string(after) != edited {
		t.Error("the refusal changed the file it refused to overwrite")
	}
}

func TestInitHasNoForceFlag(t *testing.T) {
	t.Chdir(t.TempDir())

	code, _, stderr := execute(t, "init", "--force")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "unknown flag: --force") {
		t.Errorf("stderr = %q, want an unknown flag", stderr)
	}
}

func TestInitDryRunPrintsAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	code, stdout, stderr := execute(t, "init", "--dry-run")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if stdout != StarterConfig() {
		t.Error("--dry-run printed something other than the file it would write")
	}
	if _, err := os.Stat(filepath.Join(dir, config.FileName)); err == nil {
		t.Fatal("--dry-run wrote the file")
	}

	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte("version = 1\n"), 0o600); err != nil {
		t.Fatalf("writing a configuration: %v", err)
	}
	if code, stdout, _ = execute(t, "init", "--dry-run"); code != int(mutation.ExitOK) {
		t.Errorf("--dry-run over an existing file exited %d, want 0", code)
	}
	if stdout != StarterConfig() {
		t.Error("--dry-run over an existing file printed something else")
	}
}

func TestInitCheckIsAFreshnessGate(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, config.FileName)

	code, _, stderr := execute(t, "init", "--check")
	if code != int(mutation.ExitPolicyFailure) {
		t.Errorf("exit = %d for a missing file, want 1", code)
	}
	if !strings.Contains(stderr, "error "+string(CodeConfigurationStale)) {
		t.Errorf("stderr = %q, want the stale code", stderr)
	}

	if err := os.WriteFile(path, []byte(StarterConfig()), 0o600); err != nil {
		t.Fatalf("writing the generated file: %v", err)
	}
	code, stdout, stderr := execute(t, "init", "--check")
	if code != int(mutation.ExitOK) {
		t.Errorf("exit = %d for the generated file, want 0\n%s", code, stderr)
	}
	if !strings.Contains(stdout, path) {
		t.Errorf("stdout = %q, want the path it compared", stdout)
	}

	if err := os.WriteFile(path, []byte(StarterConfig()+"\n"), 0o600); err != nil {
		t.Fatalf("editing the file: %v", err)
	}
	if code, _, _ = execute(t, "init", "--check"); code != int(mutation.ExitPolicyFailure) {
		t.Errorf("exit = %d for an edited file, want 1", code)
	}
}

func TestInitCheckAndDryRunAreExclusive(t *testing.T) {
	t.Chdir(t.TempDir())

	code, _, stderr := execute(t, "init", "--check", "--dry-run")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "if any flags in the group") {
		t.Errorf("stderr = %q, want cobra's exclusivity refusal", stderr)
	}
}
