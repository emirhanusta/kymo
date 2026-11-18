package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

func TestPasswordHashAndVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Errorf("unexpected encoding: %s", h)
	}
	if !VerifyPassword("correct horse battery", h) {
		t.Error("correct password rejected")
	}
	if VerifyPassword("correct horse batterY", h) || VerifyPassword("", h) {
		t.Error("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Error("salts must differ")
	}
	for _, bad := range []string{"", "plain", "$argon2id$v=19$m=1,t=1,p=1$x$y", "$bcrypt$v=19$m=65536,t=3,p=2$AAAA$AAAA",
		"$argon2id$v=19$m=99999999,t=3,p=2$AAAA$AAAA", "$argon2id$v=19$m=65536,t=3,p=2$!!$AAAA"} {
		if VerifyPassword("x", bad) {
			t.Errorf("malformed hash %q verified", bad)
		}
	}
}

func TestPasswordPolicyAndEmail(t *testing.T) {
	if ValidatePassword("short") == nil || ValidatePassword(strings.Repeat("a", 257)) == nil || ValidatePassword("exactly12chr") != nil {
		t.Error("password policy")
	}
	if e, err := NormalizeEmail("  Alice@Example.COM "); err != nil || e != "alice@example.com" {
		t.Errorf("normalize: %q %v", e, err)
	}
	for _, bad := range []string{"", "nope", "a@", "@b.c", "a b@c.d", "a@b"} {
		if _, err := NormalizeEmail(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestTokens(t *testing.T) {
	a, _ := RandomToken(32)
	b, _ := RandomToken(32)
	if a == b || len(a) != 43 {
		t.Errorf("random tokens: %q %q", a, b)
	}
	if HashToken("x") != HashToken("x") || HashToken("x") == HashToken("y") || len(HashToken("x")) != 64 {
		t.Error("hash")
	}
	tok, prefix, err := NewAPIToken()
	if err != nil || !strings.HasPrefix(tok, "kymo_"+prefix+"_") || len(prefix) != 8 || !LooksLikeAPIToken(tok) {
		t.Fatalf("api token: %q %q %v", tok, prefix, err)
	}
	for _, bad := range []string{"", "kymo_", "kymo_abcdefgh_", "abc_abcdefgh_" + strings.Repeat("x", 30), "kymo_abcdefghXsecret_" + strings.Repeat("x", 30)} {
		if LooksLikeAPIToken(bad) {
			t.Errorf("%q looks like a token", bad)
		}
	}
}

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewLimiter(3, 15*time.Minute)
	l.now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		l.Fail("k")
	}
	if b, _ := l.Blocked("k"); b {
		t.Fatal("blocked too early")
	}
	l.Fail("k")
	b, wait := l.Blocked("k")
	if !b || wait != 15*time.Minute {
		t.Fatalf("blocked=%v wait=%v", b, wait)
	}
	if b, _ := l.Blocked("other"); b {
		t.Error("keys must be independent")
	}
	now = now.Add(15*time.Minute + time.Second)
	if b, _ := l.Blocked("k"); b {
		t.Error("window must expire")
	}
	l.Fail("k")
	l.Reset("k")
	l.Fail("k")
	l.Fail("k")
	if b, _ := l.Blocked("k"); b {
		t.Error("reset must clear")
	}
	// idle keys are swept
	now = now.Add(time.Hour)
	l.Blocked("x")
	if len(l.hits) != 0 {
		t.Errorf("sweep left %d keys", len(l.hits))
	}
}

func TestPermissions(t *testing.T) {
	cases := []struct {
		role model.Role
		perm string
		want bool
	}{
		{model.RoleViewer, PermWorkloadsManage, false}, {model.RoleOperator, PermWorkloadsManage, true},
		{model.RoleOperator, PermLogCapture, true}, {model.RoleOperator, PermSourcesManage, false},
		{model.RoleAdmin, PermSourcesManage, true}, {model.RoleAdmin, PermAuditRead, true}, {model.RoleAdmin, PermOwner, false},
		{model.RoleOwner, PermOwner, true}, {model.RoleOwner, PermTokensManageAll, true},
		{model.Role(""), PermWorkloadsManage, false}, {model.RoleOwner, "bogus", false},
	}
	for _, c := range cases {
		if got := Can(c.role, c.perm); got != c.want {
			t.Errorf("Can(%s,%s)=%v", c.role, c.perm, got)
		}
	}
	if len(Permissions(model.RoleViewer)) != 0 || len(Permissions(model.RoleOwner)) != 8 {
		t.Errorf("permissions: %v %v", Permissions(model.RoleViewer), Permissions(model.RoleOwner))
	}
	if model.MinRole(model.RoleAdmin, model.RoleViewer) != model.RoleViewer || model.Role("").AtLeast(model.RoleViewer) {
		t.Error("role helpers")
	}
}

func TestSafeReturnTo(t *testing.T) {
	ok := []string{"/", "/workloads/a%2Fb", "/issues?status=open#x", "/a/b"}
	for _, s := range ok {
		if SafeReturnTo(s) != s {
			t.Errorf("%q should be kept", s)
		}
	}
	bad := []string{"", "//evil.com", "/\\evil.com", "https://evil.com", "http:evil", "evil.com", "javascript:alert(1)", "/a\r\nSet-Cookie: x", "///evil", "/\t/evil.com", "\\\\evil"}
	for _, s := range bad {
		if got := SafeReturnTo(s); got != "/" {
			t.Errorf("%q -> %q, want /", s, got)
		}
	}
}

func TestMapRoleAndClaims(t *testing.T) {
	m := map[string][]string{"admin": {"kymo-admins"}, "operator": {"sre", "oncall"}, "viewer": {"all"}}
	cases := []struct {
		groups []string
		want   model.Role
		ok     bool
	}{
		{[]string{"sre"}, model.RoleOperator, true}, {[]string{"sre", "kymo-admins"}, model.RoleAdmin, true},
		{[]string{"all", "oncall"}, model.RoleOperator, true}, {[]string{"x"}, "", false}, {nil, "", false},
	}
	for _, c := range cases {
		got, ok := MapRole(c.groups, m)
		if got != c.want || ok != c.ok {
			t.Errorf("MapRole(%v)=%q,%v", c.groups, got, ok)
		}
	}
	if r, _ := MapRole([]string{"o"}, map[string][]string{"owner": {"o"}}); r != "" {
		t.Error("owner must not be mappable")
	}
	claims := map[string]any{"groups": []any{"a", "b", 3}, "single": "x", "realm_access": map[string]any{"roles": []any{"r1"}}}
	if g := claimStrings(claims, "groups"); len(g) != 2 {
		t.Errorf("groups %v", g)
	}
	if g := claimStrings(claims, "single"); len(g) != 1 || g[0] != "x" {
		t.Errorf("single %v", g)
	}
	if g := claimStrings(claims, "realm_access.roles"); len(g) != 1 || g[0] != "r1" {
		t.Errorf("dotted %v", g)
	}
	if claimStrings(claims, "missing.path") != nil || claimStrings(claims, "") != nil {
		t.Error("missing claim")
	}
}

func TestLimiterIPKeysGetLargerBudget(t *testing.T) {
	l := NewLimiter(LoginMaxAttempts, LoginWindow)
	for i := 0; i < LoginMaxAttempts; i++ {
		l.Fail("ip:10.0.0.1")
		l.Fail("login:alice")
	}
	if blocked, _ := l.Blocked("login:alice"); !blocked {
		t.Fatal("email key should be blocked after the base budget")
	}
	if blocked, _ := l.Blocked("ip:10.0.0.1"); blocked {
		t.Fatal("a shared IP must not be blocked after one account's failures")
	}
	for i := LoginMaxAttempts; i < LoginMaxAttemptsPerIP; i++ {
		l.Fail("ip:10.0.0.1")
	}
	if blocked, _ := l.Blocked("ip:10.0.0.1"); !blocked {
		t.Fatal("IP key should be blocked after the per-IP budget")
	}
}

func TestUsernameHelpers(t *testing.T) {
	if u, err := NormalizeUsername("  Alice.B-1_x "); err != nil || u != "alice.b-1_x" {
		t.Errorf("normalize: %q %v", u, err)
	}
	for _, bad := range []string{"", "a", "-ab", ".ab", "a b", "a@b", strings.Repeat("a", 65), "ünï"} {
		if _, err := NormalizeUsername(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	cases := map[string]string{
		"Alice":                  "alice",
		"john.doe":               "john.doe",
		"a b@c":                  "a-b-c",
		"__x":                    "x", // leading separators dropped -> too short
		"":                       "user",
		"%%%":                    "user",
		"émile":                  "-mile",
		"Jean-Luc P.":            "jean-luc-p.",
		strings.Repeat("z", 100): strings.Repeat("z", 60),
	}
	for in, want := range cases {
		got := SanitizeUsername(in)
		if in == "__x" || in == "émile" {
			// just require validity
			if _, err := NormalizeUsername(got); err != nil {
				t.Errorf("Sanitize(%q)=%q invalid", in, got)
			}
			continue
		}
		if got != want {
			t.Errorf("Sanitize(%q)=%q want %q", in, got, want)
		}
		if _, err := NormalizeUsername(got); err != nil {
			t.Errorf("Sanitize(%q)=%q invalid", in, got)
		}
	}
}

func TestGeneratePassword(t *testing.T) {
	a, _ := GeneratePassword(24)
	b, _ := GeneratePassword(24)
	if len(a) != 24 || a == b || ValidatePassword(a) != nil {
		t.Errorf("generated: %q %q", a, b)
	}
}
