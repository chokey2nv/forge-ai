package main

import "testing"

func TestLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8080": true, "localhost:8080": true, "[::1]:8080": true,
		":8080": false, "0.0.0.0:8080": false, "192.168.1.5:8080": false, "8080": false,
	} {
		if got := loopback(addr); got != want {
			t.Errorf("%s: %v, want %v", addr, got, want)
		}
	}
}
