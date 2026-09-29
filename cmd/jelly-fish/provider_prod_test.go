//go:build !dev

package main

import "testing"

func TestNonDevBuildHasNoProvider(t *testing.T) {
	if newProvider() != nil {
		t.Fatal("non-dev build wired a provider")
	}
}
