package timescale

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
	"github.com/emirhanusta/kymo/internal/secret"
	"github.com/emirhanusta/kymo/internal/service/notify"
)

func TestChannelCRUD(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	repo := db.Notifications()

	c := &model.NotificationChannel{
		Name: "ops", Type: "slack", URLEnc: []byte{0, 1, 2, 255}, SecretEnc: nil, Enabled: true, MinSeverity: model.SeverityWarning,
		Sources: []string{"prod", "dev"}, Types: []string{"oom"}, NotifyResolved: true, IncludeLogs: false, RepeatMinutes: 15,
	}
	if err := repo.CreateChannel(ctx, c); err != nil || c.ID == 0 || c.CreatedAt.IsZero() || c.Origin != "user" {
		t.Fatalf("create: %+v %v", c, err)
	}
	got, err := repo.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "ops" || got.Type != "slack" || !bytes.Equal(got.URLEnc, []byte{0, 1, 2, 255}) || got.SecretEnc != nil ||
		!got.Enabled || got.MinSeverity != model.SeverityWarning || len(got.Sources) != 2 || got.Sources[1] != "dev" ||
		got.Namespaces == nil || len(got.Namespaces) != 0 || got.Types[0] != "oom" || !got.NotifyResolved || got.IncludeLogs ||
		got.RepeatMinutes != 15 {
		t.Errorf("get: %+v", got)
	}

	got.Name, got.SecretEnc, got.Enabled, got.Sources, got.Namespaces = "ops2", []byte("sec"), false, nil, []string{"ns"}
	if err := repo.UpdateChannel(ctx, got); err != nil {
		t.Fatal(err)
	}
	up, _ := repo.GetChannel(ctx, c.ID)
	if up.Name != "ops2" || string(up.SecretEnc) != "sec" || up.Enabled || len(up.Sources) != 0 || up.Namespaces[0] != "ns" ||
		!up.CreatedAt.Equal(c.CreatedAt) || up.UpdatedAt.Before(c.UpdatedAt) {
		t.Errorf("update: %+v", up)
	}
	if err := repo.UpdateChannel(ctx, &model.NotificationChannel{ID: 999, Type: "slack", MinSeverity: "critical"}); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("update missing: %v", err)
	}

	// Constraints.
	bad := &model.NotificationChannel{Name: "x", Type: "pager", URLEnc: []byte("u"), MinSeverity: "critical"}
	if err := repo.CreateChannel(ctx, bad); err == nil {
		t.Error("unknown type must be rejected by the database")
	}
	cfg := func(name string) *model.NotificationChannel {
		return &model.NotificationChannel{Name: name, Type: "webhook", Origin: "config", URLEnc: []byte("https://x"), MinSeverity: "critical"}
	}
	if err := repo.CreateChannel(ctx, cfg("dup")); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateChannel(ctx, cfg("dup")); err == nil {
		t.Error("config channel names are unique")
	}

	list, err := repo.ListChannels(ctx)
	if err != nil || len(list) != 2 || list[0].ID != c.ID {
		t.Fatalf("list: %v %v", list, err)
	}

	// Deliveries cascade with the channel.
	d := &model.ChannelDelivery{ChannelID: c.ID, Event: model.EventTest, OK: true, StatusCode: 200}
	if err := repo.RecordDelivery(ctx, d); err != nil || d.ID == 0 {
		t.Fatal(err)
	}
	if err := repo.DeleteChannel(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteChannel(ctx, c.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
	if n := count(t, db, "notification_deliveries", ""); n != 0 {
		t.Errorf("deliveries after cascade: %d", n)
	}
	if _, err := repo.GetChannel(ctx, c.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("get deleted: %v", err)
	}
}

func TestDeliveries(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	repo := db.Notifications()
	mk := func(name string) *model.NotificationChannel {
		c := &model.NotificationChannel{Name: name, Type: "webhook", URLEnc: []byte("u"), Enabled: true, MinSeverity: "critical"}
		if err := repo.CreateChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	a, b := mk("a"), mk("b")
	i7, i8 := int64(7), int64(8)
	rec := func(ch *model.NotificationChannel, issue *int64, ev model.IssueEventKind, ok bool, status int, errMsg string, d time.Duration) {
		t.Helper()
		if err := repo.RecordDelivery(ctx, &model.ChannelDelivery{ChannelID: ch.ID, IssueID: issue, Event: ev, OK: ok,
			StatusCode: status, Error: errMsg, Attempts: 3, SentAt: at(d)}); err != nil {
			t.Fatal(err)
		}
	}
	rec(a, &i7, model.EventOpened, true, 200, "", 1*time.Minute)
	rec(a, &i7, model.EventReminder, false, 500, "HTTP 500", 5*time.Minute)
	rec(a, &i7, model.EventResolved, true, 200, "", 9*time.Minute)
	rec(a, nil, model.EventTest, true, 200, "", 3*time.Minute)
	rec(b, &i8, model.EventOpened, true, 204, "", 2*time.Minute)
	rec(b, &i7, model.EventOpened, true, 204, "", 4*time.Minute)

	all, err := repo.ListDeliveries(ctx, 0, 50)
	if err != nil || len(all) != 6 || all[0].Event != model.EventResolved || !all[0].SentAt.Equal(at(9*time.Minute)) {
		t.Fatalf("all: %v %v", all, err)
	}
	onlyA, _ := repo.ListDeliveries(ctx, a.ID, 2)
	if len(onlyA) != 2 || onlyA[1].Event != model.EventReminder || onlyA[1].Error != "HTTP 500" || onlyA[1].Attempts != 3 {
		t.Errorf("channel a: %+v", onlyA)
	}
	if onlyA[0].IssueID == nil || *onlyA[0].IssueID != 7 {
		t.Errorf("issue id: %+v", onlyA[0])
	}
	if test := all[len(all)-1]; test.Event == model.EventTest && test.IssueID != nil {
		t.Errorf("test delivery has an issue: %+v", test)
	}

	last, err := repo.LastDeliveries(ctx)
	if err != nil || len(last) != 2 || last[a.ID].Event != model.EventResolved || last[b.ID].StatusCode != 204 || !last[b.ID].SentAt.Equal(at(4*time.Minute)) {
		t.Errorf("last deliveries: %+v %v", last, err)
	}

	// Reminder pacing only looks at opened / reminder events with an issue.
	marks, err := repo.LastIssueDeliveries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[[2]int64]time.Time{}
	for _, m := range marks {
		got[[2]int64{m.ChannelID, m.IssueID}] = m.At
	}
	if len(got) != 3 || !got[[2]int64{a.ID, 7}].Equal(at(5*time.Minute)) || !got[[2]int64{b.ID, 8}].Equal(at(2*time.Minute)) ||
		!got[[2]int64{b.ID, 7}].Equal(at(4*time.Minute)) {
		t.Errorf("marks: %v", got)
	}

	n, err := repo.DeleteDeliveriesBefore(ctx, at(4*time.Minute))
	if err != nil || n != 3 {
		t.Fatalf("cleanup deleted %d: %v", n, err)
	}
	rest, _ := repo.ListDeliveries(ctx, 0, 50)
	if len(rest) != 3 {
		t.Errorf("remaining: %d", len(rest))
	}
}

// The service seals the URL before it reaches the database and reads it back
// for sending.
func TestEncryptedURLRoundTripThroughService(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	var gotBody string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = r.URL.Path
		w.WriteHeader(200)
	}))
	defer hook.Close()

	box, _ := secret.New("integration-key")
	svc := notify.New(db.Notifications(), nil, box, nil, notify.Options{Enabled: true}, zap.NewNop())
	name, typ, url, sec := "ops", "webhook", hook.URL+"/very/secret/path", "hmac"
	v, err := svc.Create(ctx, notify.ChannelInput{Name: &name, Type: &typ, URL: &url, Secret: &sec})
	if err != nil {
		t.Fatal(err)
	}

	var urlEnc, secEnc []byte
	if err := db.pool.QueryRow(ctx, `SELECT url_enc, secret_enc FROM notification_channels WHERE id = $1`, v.ID).Scan(&urlEnc, &secEnc); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(urlEnc, []byte("secret/path")) || bytes.Contains(urlEnc, []byte("127.0.0.1")) || bytes.Contains(secEnc, []byte("hmac")) {
		t.Fatal("URL / secret stored in plain text")
	}
	if !strings.HasSuffix(v.URLHint, "/…/path") {
		t.Errorf("hint %q", v.URLHint)
	}

	res, err := svc.Test(ctx, v.ID)
	if err != nil || !res.OK || gotBody != "/very/secret/path" {
		t.Fatalf("test: %+v %v (server saw %q)", res, err, gotBody)
	}
	list, _ := svc.ListChannels(ctx)
	if len(list) != 1 || list[0].LastDelivery == nil || !list[0].LastDelivery.OK || list[0].LastDelivery.Event != model.EventTest {
		t.Errorf("list: %+v", list)
	}

	// Config channels are mirrored with the plain URL and are locked.
	if err := svc.SyncConfig(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.ListChannels(ctx); len(got) != 1 {
		t.Error("sync with nothing declared must keep user channels")
	}
}
