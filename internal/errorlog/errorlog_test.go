package errorlog

import (
	"strings"
	"testing"
	"time"
)

func mustAnalyzer(t *testing.T, extra, ignore []string) *Analyzer {
	t.Helper()
	a, err := NewAnalyzer(extra, ignore)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestIsHead(t *testing.T) {
	a := mustAnalyzer(t, nil, nil)
	cases := []struct {
		name string
		line string
		want bool
	}{
		{"json level error", `{"level":"error","msg":"boom","ts":1}`, true},
		{"json severity ERROR", `{"severity":"ERROR","message":"boom"}`, true},
		{"json lvl fatal", `{"lvl":"fatal","msg":"x"}`, true},
		{"json info mentions error", `{"level":"info","msg":"recovered from error"}`, false},
		{"json warn with error key", `{"level":"warn","msg":"retry","error":"timeout"}`, false},
		{"json no level", `{"msg":"error in message"}`, false},
		{"json pino numeric error", `{"level":50,"msg":"x"}`, true},
		{"json pino numeric info", `{"level":30,"msg":"error"}`, false},
		{"logfmt error", `ts=2024-01-02T03:04:05Z level=error msg="db down" host=a`, true},
		{"logfmt info", `ts=2024-01-02T03:04:05Z level=info msg="error rate ok"`, false},
		{"logfmt quoted level", `level="warn" msg="x error"`, false},
		{"klog error", `E0102 15:04:05.123456       1 main.go:42] failed to sync`, true},
		{"klog fatal", `F0102 15:04:05.123456       1 main.go:42] bad`, true},
		{"klog info with error text", `I0102 15:04:05.123456       1 main.go:42] error count 0`, false},
		{"klog warning", `W0102 15:04:05.123456       1 main.go:42] slow`, false},
		{"plain ERROR", `2024-01-02 10:00:00,123 ERROR com.foo.Bar - failed`, true},
		{"plain bracket", `2024-01-02 10:00:00 [error] upstream timed out`, true},
		{"plain INFO mentions ERROR later", `2024-01-02 10:00:00 INFO retrying after ERROR in upstream`, false},
		{"plain WARN with error word", `WARN something error happened`, false},
		{"plain FATAL", `FATAL: cannot start`, true},
		{"plain PANIC", `PANIC unexpected`, true},
		{"plain CRITICAL", `[CRITICAL] disk full`, true},
		{"lowercase error mid sentence", `retrying after error in upstream`, false},
		{"error colon prefix", `error: connection refused`, true},
		{"go panic", `panic: runtime error: index out of range [5] with length 3`, true},
		{"go fatal error", `fatal error: all goroutines are asleep - deadlock!`, true},
		{"python traceback", `Traceback (most recent call last):`, true},
		{"java thread exception", `Exception in thread "main" java.lang.NullPointerException: x`, true},
		{"java exception line", `java.lang.IllegalStateException: bad state`, true},
		{"node error", `TypeError: Cannot read properties of undefined`, true},
		{"node Error", `Error: boom`, true},
		{"uncaught", `Unhandled rejection: foo`, true},
		{"normal line", `GET /healthz 200 3ms`, false},
		{"err_ token", `INFO got ERR_CONNECTION_RESET`, false},
		{"empty", `   `, false},
		{"ansi colored", "\x1b[31mERROR\x1b[0m failed", true},
	}
	for _, c := range cases {
		if got := a.IsHead(c.line); got != c.want {
			t.Errorf("%s: IsHead(%q) = %v, want %v", c.name, c.line, got, c.want)
		}
	}
}

func TestExtraAndIgnorePatterns(t *testing.T) {
	a := mustAnalyzer(t, []string{`connection reset`}, []string{`healthcheck`, `(?i)ignore me`})
	if !a.IsHead("INFO connection reset by peer") {
		t.Error("extra pattern should mark an INFO line as error")
	}
	if a.IsHead("ERROR healthcheck failed") {
		t.Error("ignore pattern should discard the line")
	}
	if a.IsHead(`{"level":"error","msg":"please IGNORE ME"}`) {
		t.Error("ignore pattern should be applied to JSON lines")
	}
	if e, ok := a.Analyze([]string{"ERROR boom", "  at x", "  healthcheck"}); ok {
		t.Errorf("ignore pattern in continuation must discard group, got %+v", e)
	}
	if _, err := NewAnalyzer([]string{"("}, nil); err == nil {
		t.Error("invalid regex should fail")
	}
}

func TestAnalyzeTemplates(t *testing.T) {
	a := mustAnalyzer(t, nil, nil)
	cases := []struct {
		name  string
		lines []string
		level string
		want  string
	}{
		{"json msg", []string{`{"level":"error","msg":"order 1234 failed for user 'bob'","ts":"2024"}`}, "error", "order <n> failed for user <str>"},
		{"json message beats error", []string{`{"level":"ERROR","message":"payment failed","error":"dial tcp 10.0.0.1:5432: refused"}`}, "error", "payment failed"},
		{"json error only", []string{`{"severity":"error","error":"dial tcp 10.0.0.1:5432: refused"}`}, "error", "dial tcp <ip>: refused"},
		{"logfmt msg", []string{`ts=2024-01-02T03:04:05Z level=error msg="db 42 down" err="x"`}, "error", "db <n> down"},
		{"klog", []string{`E0102 15:04:05.123456       1 reflector.go:138] Failed to watch *v1.Pod: id 7`}, "error", "Failed to watch *v1.Pod: id <n>"},
		{"klog fatal", []string{`F0102 15:04:05.123456       1 main.go:1] bad`}, "fatal", "bad"},
		{"plain java", []string{`2024-01-02 10:00:00,123 [main] ERROR com.foo.Bar - Failed to process order 99 from 10.1.2.3:8080`}, "error", "Failed to process order <n> from <ip>"},
		{"plain level only", []string{`2024-01-02T10:00:00.123Z ERROR user a@b.com not found (req 7f3a9c2d11)`}, "error", "user <email> not found (req <hex>)"},
		{"uuid", []string{`ERROR job 123e4567-e89b-12d3-a456-426614174000 failed`}, "error", "job <uuid> failed"},
		{"path numbers", []string{`ERROR GET /api/users/123/orders/45 returned 500`}, "error", "GET /api/users/<n>/orders/<n> returned <n>"},
		{"apostrophe", []string{`ERROR can't connect to 'db-1' at "host"`}, "error", "can't connect to <str> at <str>"},
		{"timestamp inside", []string{`ERROR token expired at 2024-01-02T10:00:00Z`}, "error", "token expired at <ts>"},
		{"panic fatal", []string{`panic: runtime error: index out of range [5] with length 3`, ``, `goroutine 1 [running]:`}, "fatal", "panic: runtime error: index out of range [<n>] with length <n>"},
		{"python", []string{`Traceback (most recent call last):`, `  File "/app/x.py", line 3, in <module>`, `    foo()`, `ValueError: bad value 42`}, "error", "ValueError: bad value <n>"},
		{"java thread", []string{`Exception in thread "main" java.lang.NullPointerException: Cannot invoke "x" on null`, "\tat a.B.c(B.java:1)"}, "error", "java.lang.NullPointerException: Cannot invoke <str> on null"},
		{"node", []string{`TypeError: Cannot read properties of undefined (reading 'id')`, `    at foo (/app/a.js:1:2)`}, "error", "TypeError: Cannot read properties of undefined (reading <str>)"},
		{"capped", []string{"ERROR " + strings.Repeat("word ", 200)}, "error", ""},
	}
	for _, c := range cases {
		e, ok := a.Analyze(c.lines)
		if !ok {
			t.Errorf("%s: not an error", c.name)
			continue
		}
		if e.Level != c.level {
			t.Errorf("%s: level = %s, want %s", c.name, e.Level, c.level)
		}
		if c.want != "" && e.Template != c.want {
			t.Errorf("%s: template = %q, want %q", c.name, e.Template, c.want)
		}
		if n := len([]rune(e.Template)); n > MaxTemplateLen {
			t.Errorf("%s: template too long: %d", c.name, n)
		}
	}
}

func TestAnalyzeNotError(t *testing.T) {
	a := mustAnalyzer(t, nil, nil)
	if _, ok := a.Analyze([]string{`{"level":"info","msg":"error"}`}); ok {
		t.Error("info JSON is not an error")
	}
	if _, ok := a.Analyze(nil); ok {
		t.Error("empty group")
	}
}

func TestSameTemplateDifferentValues(t *testing.T) {
	a := mustAnalyzer(t, nil, nil)
	x, _ := a.Analyze([]string{`2024-01-02 10:00:00 ERROR failed to load user 17 from 10.0.0.1:80`})
	y, _ := a.Analyze([]string{`2025-05-06 11:11:11 ERROR failed to load user 9001 from 10.9.9.9:81`})
	if x.Template != y.Template {
		t.Errorf("templates differ: %q vs %q", x.Template, y.Template)
	}
	if SignatureID("w", x.Template) != SignatureID("w", y.Template) || SignatureID("w", x.Template) == SignatureID("v", x.Template) {
		t.Error("signature id must depend on workload and template only")
	}
	if len(SignatureID("w", "t")) != 16 {
		t.Error("id must be 16 hex chars")
	}
}

func TestMask(t *testing.T) {
	cases := []struct{ in, want string }{
		{`Authorization: Bearer abc123def456ghi`, `Authorization: Bearer ***`},
		{`header Bearer abc123def456ghi sent`, `header Bearer *** sent`},
		{`Basic dXNlcjpwYXNzd29yZA== bad`, `Basic *** bad`},
		{`password=hunter2 user=bob`, `password=*** user=bob`},
		{`login?user=a&api_key=XYZ123&x=1`, `login?user=a&api_key=***&x=1`},
		{`{"password":"hunter2","user":"bob"}`, `{"password":"***","user":"bob"}`},
		{`secret: 's3cr3t'`, `secret: '***'`},
		{`client_secret=abc token=def`, `client_secret=*** token=***`},
		{`key AKIAIOSFODNN7EXAMPLE leaked`, `key *** leaked`},
		{`jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.abc_def-ghi ok`, `jwt *** ok`},
		{`card 4111111111111111 declined`, `card *** declined`},
		{`card 4111 1111 1111 1111 declined`, `card *** declined`},
		{`postgres://user:pass@host:5432/db`, `postgres://***@host:5432/db`},
		{`nothing secret here, 12345`, `nothing secret here, 12345`},
	}
	for _, c := range cases {
		if got := Mask(c.in); got != c.want {
			t.Errorf("Mask(%q) = %q, want %q", c.in, got, c.want)
		}
		if again := Mask(Mask(c.in)); again != Mask(c.in) {
			t.Errorf("Mask not idempotent for %q: %q", c.in, again)
		}
	}
}

func TestAnalyzeMasksEverythingStored(t *testing.T) {
	a := mustAnalyzer(t, nil, nil)
	e, ok := a.Analyze([]string{`ERROR login failed password=hunter2`, `  at x`, `  Authorization: Bearer abc123def456ghi`})
	if !ok {
		t.Fatal("expected error")
	}
	for _, s := range []string{e.Template, e.Sample, e.Message} {
		if strings.Contains(s, "hunter2") || strings.Contains(s, "abc123def456ghi") {
			t.Errorf("secret leaked: %q", s)
		}
	}
}

func TestAnalyzeMessageCapped(t *testing.T) {
	a := mustAnalyzer(t, nil, nil)
	e, _ := a.Analyze([]string{"ERROR " + strings.Repeat("x", 20000)})
	if len(e.Message) > MaxMessageLen {
		t.Errorf("message not capped: %d", len(e.Message))
	}
}

type feed struct {
	g   *Grouper
	a   *Analyzer
	out []Group
	t   time.Time
}

func (f *feed) add(lines ...string) {
	for _, l := range lines {
		f.t = f.t.Add(time.Millisecond)
		f.out = append(f.out, f.g.Add(f.a, l, f.t, f.t)...)
	}
}

func newFeed(t *testing.T) *feed {
	return &feed{g: NewGrouper(0, 0), a: mustAnalyzer(t, nil, nil), t: time.Unix(1700000000, 0)}
}

func TestGrouping(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  []int // lines per group
	}{
		{"java trace", []string{
			"2024-01-02 ERROR boom", "java.lang.RuntimeException: x", "\tat a.B.c(B.java:1)", "\tat a.B.d(B.java:2)",
			"Caused by: java.io.IOException: y", "\tat e.F.g(F.java:3)", "\t... 5 more", "INFO next"}, []int{7}},
		{"node trace", []string{"Error: boom", "    at foo (/a.js:1:2)", "    at bar (/a.js:3:4)", "normal"}, []int{3}},
		{"python", []string{"Traceback (most recent call last):", `  File "x.py", line 1, in <module>`, "    foo()", "ValueError: bad", "next line"}, []int{4}},
		{"python chained", []string{"Traceback (most recent call last):", `  File "x.py", line 1`, "KeyError: 'a'", "", "During handling of the above exception, another exception occurred:", "", "Traceback (most recent call last):", `  File "x.py", line 2`, "ValueError: b", "ok"}, nil},
		{"go panic", []string{"panic: oops", "", "goroutine 1 [running]:", "main.main()", "\t/app/main.go:10 +0x1d", "exit status 2", "next"}, []int{6}},
		{"two errors", []string{"ERROR a", "ERROR b", "INFO c"}, []int{1, 1}},
		{"non error ignored", []string{"INFO a", "  indented after info", "DEBUG b"}, nil},
		{"log head then exception class", []string{"ERROR failed", "java.lang.NullPointerException: x", "\tat q"}, []int{3}},
		{"at line continues", []string{"ERROR boom", "at foo.bar(Baz.java:1)", "next"}, []int{2}},
	}
	for _, c := range cases {
		f := newFeed(t)
		f.add(c.lines...)
		f.out = append(f.out, f.g.Flush()...)
		if c.name == "python chained" {
			if len(f.out) != 1 || len(f.out[0].Lines) != 9 {
				t.Errorf("%s: got %d groups %v", c.name, len(f.out), f.out)
			}
			continue
		}
		if len(f.out) != len(c.want) {
			t.Errorf("%s: %d groups, want %d (%v)", c.name, len(f.out), len(c.want), f.out)
			continue
		}
		for i, n := range c.want {
			if len(f.out[i].Lines) != n {
				t.Errorf("%s: group %d has %d lines, want %d: %q", c.name, i, len(f.out[i].Lines), n, f.out[i].Lines)
			}
		}
	}
}

func TestGroupLineCapAndIdle(t *testing.T) {
	f := newFeed(t)
	f.add("ERROR boom")
	for i := 0; i < 80; i++ {
		f.add("\tat x.Y(Z.java:1)")
	}
	f.add("INFO done")
	if len(f.out) != 1 || len(f.out[0].Lines) != MaxGroupLines || !f.out[0].Truncated {
		t.Fatalf("expected one truncated group of %d lines, got %d groups", MaxGroupLines, len(f.out))
	}

	g := NewGrouper(0, 200*time.Millisecond)
	a := mustAnalyzer(t, nil, nil)
	now := time.Unix(1700000000, 0)
	g.Add(a, "ERROR x", now, now)
	if got := g.Idle(now.Add(100 * time.Millisecond)); got != nil {
		t.Error("closed too early")
	}
	if d, ok := g.NextDeadline(now.Add(50 * time.Millisecond)); !ok || d != 150*time.Millisecond {
		t.Errorf("deadline = %v %v", d, ok)
	}
	if got := g.Idle(now.Add(250 * time.Millisecond)); len(got) != 1 {
		t.Error("should close after idle")
	}
	if g.Pending() {
		t.Error("nothing should be pending")
	}
}
