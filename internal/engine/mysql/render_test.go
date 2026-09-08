package mysql

import (
	"path/filepath"
	"testing"

	"github.com/dbpod-io/dbpod/internal/contract"
	"github.com/dbpod-io/dbpod/internal/engine"
)

// Values (paths, SQL) are injected per token: a value containing spaces
// must stay a single argv element, and a value containing template
// syntax must never be re-rendered.
func TestRenderCommandValueSafety(t *testing.T) {
	spacyDir := filepath.Join(t.TempDir(), "with space")
	e := New(DefaultProfile())
	opts := engine.Options{DataDir: spacyDir, BinDir: filepath.Join(spacyDir, "bin"), Port: 3307}

	argv, err := renderCommand(e.prof().Start, e.templateContext(opts))
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 2 {
		t.Fatalf("argv = %q", argv)
	}
	if argv[1] != "--defaults-file="+filepath.Join(spacyDir, "my.cnf") {
		t.Errorf("config token = %q, want the spaced path as ONE argv element", argv[1])
	}

	// SQL containing template syntax is injected as a value: never
	// re-scanned, never evaluated
	sql := "{{ 1+1 }} 'quoted --evil'"
	argv, err = renderCommand(e.prof().Exec, func() pongoContext {
		ctx := e.templateContext(opts)
		ctx[contract.SQL] = sql
		return ctx
	}())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(argv); n != 9 || argv[n-1] != sql {
		t.Errorf("sql token = %q (argv %q), want the raw string as the last element", argv[n-1], argv)
	}
}

// host follows the bind address; wildcard binds fall back to the loopback.
func TestHostVariableFollowsBind(t *testing.T) {
	dir := t.TempDir()
	e := New(DefaultProfile())
	cases := map[string]string{
		"":         "127.0.0.1",
		"127.0.0.1": "127.0.0.1",
		"0.0.0.0":  "127.0.0.1",
		"*":        "127.0.0.1",
		"192.168.1.5": "192.168.1.5",
	}
	for bind, want := range cases {
		opts := engine.Options{DataDir: dir, BinDir: filepath.Join(dir, "bin"), Port: 3307, BindAddress: bind}
		argv, err := renderCommand(e.prof().Client, e.templateContext(opts))
		if err != nil {
			t.Fatal(err)
		}
		if i := indexOf(argv, want); i < 0 {
			t.Errorf("bind %q: argv %q has no host %q", bind, argv, want)
		}
	}
}

func indexOf(argv []string, s string) int {
	for i, a := range argv {
		if a == s {
			return i
		}
	}
	return -1
}

// pongoContext aliases the template context type for readability in tests.
type pongoContext = map[string]any

func TestBinaryNamesFromTemplates(t *testing.T) {
	e := New(validMariaDBProfile())
	server, client, admin := e.BinaryNames()
	if server != "mariadbd" || client != "mariadb" || admin != "mariadb-admin" {
		t.Errorf("binary names = %s/%s/%s", server, client, admin)
	}
}

func TestDefaultProfileBinaryNames(t *testing.T) {
	e := &Engine{}
	server, client, admin := e.BinaryNames()
	if server != "mysqld" || client != "mysql" || admin != "mysqladmin" {
		t.Errorf("default binary names = %s/%s/%s", server, client, admin)
	}
}

func validMariaDBProfile() contract.EngineProfile {
	p := contract.EngineProfile{
		ContractVersion: contract.Version,
		Engine:          "mariadb",
		Init:            "mariadb-install-db --defaults-file={{ config_path }} --auth-root-authentication-method=normal",
		Start:           "mariadbd --defaults-file={{ config_path }}",
		Client:          "mariadb -u root -h {{ host }} -P {{ port }}",
		Exec:            "mariadb -u root -h {{ host }} -P {{ port }} -e {{ sql }}",
		Shutdown:        "mariadb-admin -u root -h {{ host }} -P {{ port }} shutdown",
	}
	p.Config = "[mysqld]\n"
	return p
}
