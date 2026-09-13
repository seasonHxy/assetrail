package main

import "testing"

func TestDemo(t *testing.T) {
	if err := run(false); err != nil {
		t.Fatal(err)
	}
	if err := run(true); err != nil {
		t.Fatal(err)
	}
}
