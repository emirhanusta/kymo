package timescale

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/emirhanusta/kymo/migrations"
)

func TestSplitSQLStatements(t *testing.T) {
	script := `-- header comment; with a semicolon
CREATE TABLE a (x TEXT DEFAULT 'semi;colon');
/* block; comment */
DO $$
BEGIN
    PERFORM 1; PERFORM 2;
END
$$;
SELECT $tag$ body ; here $tag$;
-- trailing comment only
`
	got := splitSQLStatements(script)
	if len(got) != 3 {
		t.Fatalf("got %d statements, want 3: %q", len(got), got)
	}
	if !strings.Contains(got[0], "'semi;colon'") {
		t.Errorf("quoted semicolon split: %q", got[0])
	}
	if !strings.Contains(got[1], "PERFORM 1; PERFORM 2;") {
		t.Errorf("dollar-quoted body split: %q", got[1])
	}
	if !strings.HasSuffix(got[2], "$tag$") {
		t.Errorf("tagged dollar quote mangled: %q", got[2])
	}
}

func TestEmbeddedMigrationsSplitCleanly(t *testing.T) {
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no embedded migrations: %v", err)
	}
	for _, f := range files {
		body, err := fs.ReadFile(migrations.FS, f)
		if err != nil {
			t.Fatal(err)
		}
		stmts := splitSQLStatements(string(body))
		if len(stmts) == 0 {
			t.Errorf("%s: no statements", f)
		}
		for _, s := range stmts {
			if strings.Count(s, "$$")%2 != 0 {
				t.Errorf("%s: unbalanced dollar quote in %q", f, s)
			}
		}
	}
}

func TestEscapeLike(t *testing.T) {
	if got := escapeLike(`50%_off\`); got != `50\%\_off\\` {
		t.Fatalf("escapeLike = %q", got)
	}
}

func TestSanitizeContainerSort(t *testing.T) {
	if f, d := sanitizeContainerSort("score", "asc"); f != "ls.score" || d != "ASC" {
		t.Errorf("got %s %s", f, d)
	}
	if f, d := sanitizeContainerSort("1; DROP TABLE containers", "sideways"); f != "c.last_seen_at" || d != "DESC" {
		t.Errorf("unsafe input not sanitised: %s %s", f, d)
	}
}
