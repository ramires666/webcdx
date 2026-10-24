package main

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var browserProfileID = regexp.MustCompile(`^profile_[a-f0-9]{32}$`)

func validBrowserAlias(alias string) bool {
	return utf8.ValidString(alias) && utf8.RuneCountInString(alias) <= 64 && !strings.ContainsFunc(alias, unicode.IsControl) && alias == strings.TrimSpace(alias)
}

func (b *browserManager) identifyBrowser(link *browserBridge, message browserBridgeMessage) bool {
	if message.CommandProtocol < 0 || message.CommandProtocol > browserCommandProtocol {
		return false
	}
	if !browserProfileID.MatchString(message.ProfileID) || !validBrowserAlias(message.Alias) {
		return false
	}
	switch message.Product {
	case "chrome", "edge", "chromium", "unknown":
	default:
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, other := range b.bridges {
		if other == link {
			continue
		}
		other.mu.Lock()
		duplicate := other.profileID == message.ProfileID && !other.closed
		other.mu.Unlock()
		if duplicate {
			return false
		}
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	if link.profileID != "" && link.profileID != message.ProfileID {
		return false
	}
	link.profileID, link.alias, link.product = message.ProfileID, message.Alias, message.Product
	link.commandProtocol = message.CommandProtocol
	return true
}

// Caller holds b.mu. The legacy pointer supports old extensions and direct
// test fixtures, but is never a fallback when multiple connections exist.
func (b *browserManager) personalConnections() []*browserBridge {
	links := make([]*browserBridge, 0, len(b.bridges))
	for _, link := range b.bridges {
		links = append(links, link)
	}
	if len(links) == 0 && b.bridge != nil {
		links = append(links, b.bridge)
	}
	return links
}

func personalTargetOrigin(args map[string]any) (string, error) {
	raw, _ := optionalString(args, "target_origin")
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", toolErr("invalid_arguments", "target_origin must be an HTTP/HTTPS origin without credentials, path, query or fragment")
	}
	return strings.ToLower(u.Scheme + "://" + u.Host + "/"), nil
}

func (b *browserManager) choosePersonal(args map[string]any, origin string) (*browserBridge, error) {
	matched := []*browserBridge{}
	for _, link := range b.personalConnections() {
		link.mu.Lock()
		match := !link.closed
		for field, value := range map[string]string{"connection_id": link.id, "profile_id": link.profileID, "browser_alias": link.alias, "preferred_browser": link.product} {
			if wanted, _ := optionalString(args, field); wanted != "" && wanted != value {
				match = false
			}
		}
		if origin != "" {
			found := false
			for _, tab := range link.tabs {
				if strings.ToLower(browserDiagnosticURL(tab.URL)) == origin {
					found = true
				}
			}
			match = match && found
		}
		link.mu.Unlock()
		if match {
			matched = append(matched, link)
		}
	}
	if len(matched) == 0 {
		return nil, toolErr("browser_not_found", "no connected personal browser matches; use browser_control action=discover")
	}
	if len(matched) != 1 {
		return nil, toolErr("ambiguous_browser", "multiple personal browsers match; choose a profile_id or local alias from discovery")
	}
	return matched[0], nil
}

func (b *browserManager) discoverPersonal() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	browsers := []map[string]any{}
	for _, link := range b.personalConnections() {
		link.mu.Lock()
		if link.closed {
			link.mu.Unlock()
			continue
		}
		tabs := []map[string]any{}
		for _, tab := range link.tabs {
			tabs = append(tabs, map[string]any{"url": safeBrowserURL(tab.URL), "title": tab.Title, "window_id": tab.WindowID, "active": tab.Active})
		}
		product := link.product
		if product == "" {
			product = "unknown"
		}
		browsers = append(browsers, map[string]any{"connection_id": link.id, "profile_id": link.profileID, "alias": link.alias, "product": product, "paused": link.paused, "identified": link.profileID != "", "tabs": tabs})
		link.mu.Unlock()
	}
	sort.Slice(browsers, func(i, j int) bool {
		return browsers[i]["connection_id"].(string) < browsers[j]["connection_id"].(string)
	})
	health, reason := b.healthLocked()
	result := map[string]any{"status": "ready", "truncated": false, "browsers": browsers, "health": health, "health_reason": reason}
	for {
		encoded, _ := json.Marshal(result)
		if len(encoded) <= 64<<10 {
			break
		}
		largest := -1
		count := 0
		for i, item := range browsers {
			if n := len(item["tabs"].([]map[string]any)); n > count {
				largest = i
				count = n
			}
		}
		if largest < 0 {
			break
		} // At most eight identity records fit without tabs.
		browsers[largest]["tabs"] = browsers[largest]["tabs"].([]map[string]any)[:count/2]
		result["truncated"] = true
	}
	return result
}
