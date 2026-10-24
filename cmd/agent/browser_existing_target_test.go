package main

import (
	"errors"
	"testing"
)

func TestChooseBrowserExistingTabActiveAndMatchCount(t *testing.T) {
	tabs := []browserPersonalTab{
		{ID: 12, WindowID: 3, URL: "https://example.test/path?q=1#part"},
		{ID: 9, WindowID: 2, URL: "https://example.test/path?q=1#part", Active: true},
		{ID: 4, WindowID: 1, URL: "https://example.test/path?q=1#part"},
	}
	choice, err := chooseBrowserExistingTab(tabs, "https://example.test/path?q=1#part", 0)
	if err != nil {
		t.Fatal(err)
	}
	if choice.Tab.ID != 9 || choice.Matches != 3 || choice.Reason != "exact_url_active" {
		t.Fatalf("choice = %#v", choice)
	}
}

func TestChooseBrowserExistingTabDeterministicTieBreak(t *testing.T) {
	tabs := []browserPersonalTab{
		{ID: 15, WindowID: 2, URL: "https://example.test/path"},
		{ID: 8, WindowID: 1, URL: "https://example.test/path"},
		{ID: 3, WindowID: 1, URL: "https://example.test/path"},
	}
	choice, err := chooseBrowserExistingTab(tabs, "https://example.test/path", 0)
	if err != nil {
		t.Fatal(err)
	}
	if choice.Tab.ID != 3 || choice.Matches != 3 || choice.Reason != "exact_url_deterministic" {
		t.Fatalf("choice = %#v", choice)
	}
}

func TestChooseBrowserExistingTabMatchesExactPathQueryAndFragment(t *testing.T) {
	tabs := []browserPersonalTab{
		{ID: 1, WindowID: 1, URL: "https://example.test/a?x=1#part"},
		{ID: 2, WindowID: 1, URL: "https://example.test/a?x=2#part"},
		{ID: 3, WindowID: 1, URL: "http://example.test/a?x=1#part"},
	}
	choice, err := chooseBrowserExistingTab(tabs, "https://example.test/a?x=1#part", 0)
	if err != nil {
		t.Fatal(err)
	}
	if choice.Tab.ID != 1 || choice.Matches != 1 || choice.Reason != "exact_url_unique" {
		t.Fatalf("exact URL choice = %#v", choice)
	}
	_, err = chooseBrowserExistingTab(tabs, "https://example.test/", 0)
	assertBrowserExistingCode(t, err, "tab_not_found")
}

func TestChooseBrowserExistingTabCanonicalHostQueryAndWindow(t *testing.T) {
	tabs := []browserPersonalTab{
		{ID: 0, WindowID: 1, URL: "https://example.test/?q=one"},
		{ID: 7, WindowID: 2, URL: "https://example.test/?q=one"},
		{ID: 8, WindowID: 3, URL: "https://example.test/?q=two"},
	}
	choice, err := chooseBrowserExistingTab(tabs, "HTTPS://EXAMPLE.TEST?q=one", 2)
	if err != nil {
		t.Fatal(err)
	}
	if choice.Tab.ID != 7 || choice.Matches != 1 || choice.Reason != "exact_url_unique" {
		t.Fatalf("canonical URL choice = %#v", choice)
	}
	_, err = chooseBrowserExistingTab(tabs, "https://example.test/?q=one", 9)
	assertBrowserExistingCode(t, err, "tab_not_found")
}

func TestChooseBrowserExistingTabRejectsCredentialAndNonWebURLs(t *testing.T) {
	for _, exactURL := range []string{
		"https://user:pass@example.test/path",
		"file:///tmp/secret",
		"javascript:alert(1)",
		"https:///missing-host",
	} {
		_, err := chooseBrowserExistingTab(nil, exactURL, 0)
		assertBrowserExistingCode(t, err, "invalid_arguments")
	}
}

func assertBrowserExistingCode(t *testing.T, err error, want string) {
	t.Helper()
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != want {
		t.Fatalf("error = %v; want code %s", err, want)
	}
}
