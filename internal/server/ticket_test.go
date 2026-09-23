package server

import (
	"testing"
	"time"
)

func TestTicketStore_CreateAndConsume(t *testing.T) {
	ts := NewTicketStore()
	tk := ts.Create("t_123", "sess_1", "http://example.magic", nil, -1, "pg_1")
	if tk.ID != "t_123" {
		t.Fatalf("expected ID t_123, got %s", tk.ID)
	}

	// First consume should succeed
	consumed, ok := ts.Consume("t_123")
	if !ok || consumed == nil {
		t.Fatalf("expected consume to succeed")
	}
	if consumed.SessionID != "sess_1" || consumed.URL != "http://example.magic" {
		t.Errorf("consumed ticket mismatch: %+v", consumed)
	}

	// Second consume should fail (one-time use)
	_, ok = ts.Consume("t_123")
	if ok {
		t.Errorf("expected second consume to fail")
	}
}

func TestTicketStore_ExpireAndSweep(t *testing.T) {
	ts := NewTicketStore()
	tk := ts.Create("t_exp", "sess_1", "http://example.magic", nil, -1, "")
	// Artificially expire the ticket
	tk.ExpiresAt = time.Now().Add(-1 * time.Second)

	// Consume should fail on expired ticket
	_, ok := ts.Consume("t_exp")
	if ok {
		t.Errorf("expected consume to fail for expired ticket")
	}

	// Create another expired ticket and verify Sweep removes it
	tk2 := ts.Create("t_sweep", "sess_1", "http://example.magic", nil, -1, "")
	tk2.ExpiresAt = time.Now().Add(-1 * time.Second)

	ts.Sweep()
	ts.mu.Lock()
	_, found := ts.tickets["t_sweep"]
	ts.mu.Unlock()
	if found {
		t.Errorf("expected ticket to be swept")
	}
}

func TestTicketStore_CapacityEviction(t *testing.T) {
	ts := NewTicketStore()
	for i := 0; i < ticketMax+10; i++ {
		ts.Create(randomID("t_"), "sess_1", "http://example.magic", nil, -1, "")
	}
	ts.mu.Lock()
	count := len(ts.tickets)
	ts.mu.Unlock()
	if count > ticketMax {
		t.Errorf("ticket count %d exceeds ticketMax %d", count, ticketMax)
	}
}
