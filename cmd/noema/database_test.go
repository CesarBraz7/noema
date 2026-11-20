package main

import "testing"

func TestDatabaseURLPrefersNeonUnpooledConnection(t *testing.T) {
	t.Setenv("DATABASE_URL_UNPOOLED", "direct")
	t.Setenv("DATABASE_URL", "fallback")
	if got := databaseURL(); got != "direct" {
		t.Fatalf("databaseURL() = %q, esperado direct", got)
	}
	t.Setenv("DATABASE_URL_UNPOOLED", "")
	if got := databaseURL(); got != "fallback" {
		t.Fatalf("databaseURL() = %q, esperado fallback", got)
	}
}
