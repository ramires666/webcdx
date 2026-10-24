package main

import (
	"errors"
	"net/url"
	"sort"
	"strings"
)

type browserExistingChoice struct {
	Tab     browserPersonalTab
	Matches int
	Reason  string
}

func chooseBrowserExistingTab(tabs []browserPersonalTab, exactURL string, windowID int) (browserExistingChoice, error) {
	if windowID < 0 {
		return browserExistingChoice{}, toolErr("invalid_arguments", "window_id must be zero or greater")
	}
	wanted, err := canonicalBrowserExistingURL(exactURL)
	if err != nil {
		return browserExistingChoice{}, toolErr("invalid_arguments", "exact_url must be an absolute HTTP/HTTPS URL without credentials")
	}
	matches := make([]browserPersonalTab, 0)
	for _, tab := range tabs {
		if tab.ID <= 0 || windowID > 0 && tab.WindowID != windowID {
			continue
		}
		candidate, err := canonicalBrowserExistingURL(tab.URL)
		if err == nil && candidate == wanted {
			matches = append(matches, tab)
		}
	}
	if len(matches) == 0 {
		return browserExistingChoice{}, toolErr("tab_not_found", "no approved browser tab matches the requested URL and window")
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Active != matches[j].Active {
			return matches[i].Active
		}
		if matches[i].WindowID != matches[j].WindowID {
			return matches[i].WindowID < matches[j].WindowID
		}
		return matches[i].ID < matches[j].ID
	})
	reason := "exact_url_unique"
	if len(matches) > 1 {
		active := 0
		for _, tab := range matches {
			if tab.Active {
				active++
			}
		}
		if active == 1 {
			reason = "exact_url_active"
		} else {
			reason = "exact_url_deterministic"
		}
	}
	return browserExistingChoice{Tab: matches[0], Matches: len(matches), Reason: reason}, nil
}

func canonicalBrowserExistingURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.Host == "" {
		return "", errors.New("invalid browser URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("invalid browser URL")
	}
	u.Scheme = scheme
	u.Host = strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}
