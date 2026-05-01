package server

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

// fakeAuditWriter captures every Append call.
type fakeAuditWriter struct {
	calls   []fakeAppend
	nextErr error
}

type fakeAppend struct {
	action string
	data   map[string]any
}

func (f *fakeAuditWriter) Append(_ context.Context, action string, data map[string]any) error {
	f.calls = append(f.calls, fakeAppend{action: action, data: data})
	return f.nextErr
}
func (f *fakeAuditWriter) Run(_ context.Context) error { return nil }

func TestAuditAdapter_TranslatesAuditEventToActionAndDetail(t *testing.T) {
	t.Parallel()
	w := &fakeAuditWriter{}
	a := NewChassisAuditAdapter(w)
	addr := netip.MustParseAddr("100.64.1.5")
	ev := AuditEvent{
		Type:      "claim_outcome",
		At:        time.Unix(0, 0),
		RequestID: "rq_123",
		ClientIP:  addr,
		Detail: map[string]string{
			"outcome": "approved",
			"jti":     "abc",
		},
	}
	if err := a.Write(context.Background(), ev); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(w.calls) != 1 {
		t.Fatalf("calls=%d want 1", len(w.calls))
	}
	c := w.calls[0]
	if c.action != "claim_outcome" {
		t.Fatalf("action=%q", c.action)
	}
	if c.data["outcome"] != "approved" {
		t.Fatalf("outcome=%v", c.data["outcome"])
	}
	if c.data["request_id"] != "rq_123" {
		t.Fatalf("request_id=%v", c.data["request_id"])
	}
	if c.data["client_ip"] != "100.64.1.5" {
		t.Fatalf("client_ip=%v", c.data["client_ip"])
	}
}

func TestAuditAdapter_OmitsEmptyRequestIDAndInvalidClientIP(t *testing.T) {
	t.Parallel()
	w := &fakeAuditWriter{}
	a := NewChassisAuditAdapter(w)
	ev := AuditEvent{
		Type:   "x",
		At:     time.Unix(0, 0),
		Detail: map[string]string{"k": "v"},
	}
	if err := a.Write(context.Background(), ev); err != nil {
		t.Fatalf("Write: %v", err)
	}
	c := w.calls[0]
	if _, has := c.data["request_id"]; has {
		t.Fatal("request_id should not be present when AuditEvent.RequestID is empty")
	}
	if _, has := c.data["client_ip"]; has {
		t.Fatal("client_ip should not be present when AuditEvent.ClientIP is invalid")
	}
}

func TestAuditAdapter_PreservesPreSetKeys(t *testing.T) {
	t.Parallel()
	w := &fakeAuditWriter{}
	a := NewChassisAuditAdapter(w)
	addr := netip.MustParseAddr("100.64.1.5")
	ev := AuditEvent{
		Type:      "x",
		RequestID: "FROM_EVENT",
		ClientIP:  addr,
		Detail: map[string]string{
			"request_id": "FROM_DETAIL",
			"client_ip":  "FROM_DETAIL_IP",
		},
	}
	if err := a.Write(context.Background(), ev); err != nil {
		t.Fatalf("Write: %v", err)
	}
	c := w.calls[0]
	if c.data["request_id"] != "FROM_DETAIL" {
		t.Fatalf("Detail's request_id was overwritten: %v", c.data["request_id"])
	}
	if c.data["client_ip"] != "FROM_DETAIL_IP" {
		t.Fatalf("Detail's client_ip was overwritten: %v", c.data["client_ip"])
	}
}

func TestAuditAdapter_PassesThroughError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("sentinel-fail") //nolint:err113 // test fixture sentinel
	w := &fakeAuditWriter{nextErr: sentinel}
	a := NewChassisAuditAdapter(w)
	if err := a.Write(context.Background(), AuditEvent{Type: "x"}); !errors.Is(err, sentinel) {
		t.Fatalf("Write err = %v; want sentinel", err)
	}
}
