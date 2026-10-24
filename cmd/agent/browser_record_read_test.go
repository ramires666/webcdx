package main

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func readRecords(t *testing.T, f *browserFixture, args map[string]any) (map[string]any, error) {
	t.Helper()
	tab := f.b.sessions[f.session].tabs[f.tab]
	result, err := f.b.readBrowserRecords(f.ctx, tab, args)
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func TestBrowserRecordReadPagesRecordsAndAssociatesAttachments(t *testing.T) {
	body := `<aside><article class="record" id="sidebar">sidebar secret</article></aside><main id="feed">`
	for i := 1; i <= 5; i++ {
		attachments := ""
		if i == 2 {
			attachments = `<a id="file-two" data-attachment-id="attach-two" download="report.txt" href="https://files.example.test/report.txt?signature=SECRET#part">report</a><img src="https://cdn.example.test/image.png?token=SECRET" alt="image">`
		}
		body += fmt.Sprintf(`<article class="record" id="record-%d"><span class="author">Author %d</span><time class="time" datetime="2026-10-25T10:00:00Z">10:00</time><p class="body">Record %d body</p>%s</article>`, i, i, i, attachments)
	}
	body += `</main>`
	f := newBrowserFixture(t, body)
	base := map[string]any{"container_selector": "#feed", "record_selector": ".record", "text_selector": ".body", "author_selector": ".author", "time_selector": ".time", "page_size": float64(2)}
	first, err := readRecords(t, f, base)
	if err != nil {
		t.Fatal(err)
	}
	if first["total_records"] != float64(5) || first["has_more"] != true || first["collection_complete"] != false || first["status"] != "completed" {
		t.Fatalf("first page summary = %#v", first)
	}
	page1 := first["records"].([]any)
	if len(page1) != 2 {
		t.Fatalf("first page records = %#v", page1)
	}
	if page1[0].(map[string]any)["record_id"] != "record-1" || page1[0].(map[string]any)["text"] != "Record 1 body" {
		t.Fatalf("first record = %#v", page1[0])
	}
	record2 := page1[1].(map[string]any)
	attachments := record2["attachments"].([]any)
	if record2["author"] != "Author 2" || record2["timestamp"] != "2026-10-25T10:00:00Z" || len(attachments) != 2 {
		t.Fatalf("record with attachments = %#v", record2)
	}
	firstAttachment := attachments[0].(map[string]any)
	secondAttachment := attachments[1].(map[string]any)
	if firstAttachment["attachment_id"] != "attach-two" || firstAttachment["id_source"] != "stable_id" || firstAttachment["filename"] != "report.txt" || firstAttachment["url"] != "https://files.example.test/report.txt" {
		t.Fatalf("first attachment = %#v", firstAttachment)
	}
	if secondAttachment["attachment_id"] != "2" || secondAttachment["id_source"] != "ordinal" || secondAttachment["filename"] != "image.png" || secondAttachment["url"] != "https://cdn.example.test/image.png" {
		t.Fatalf("second attachment = %#v", secondAttachment)
	}
	if strings.Contains(fmt.Sprint(first), "SECRET") || strings.Contains(fmt.Sprint(first), "sidebar secret") {
		t.Fatalf("record output leaked signed URL or outside text: %#v", first)
	}

	token := first["next_page_token"].(string)
	second, err := readRecords(t, f, map[string]any{"container_selector": "#feed", "record_selector": ".record", "text_selector": ".body", "author_selector": ".author", "time_selector": ".time", "page_size": float64(2), "page_token": token})
	if err != nil {
		t.Fatal(err)
	}
	page2 := second["records"].([]any)
	if len(page2) != 2 || page2[0].(map[string]any)["record_id"] != "record-3" || page2[1].(map[string]any)["record_id"] != "record-4" {
		t.Fatalf("second page records = %#v", page2)
	}
	third, err := readRecords(t, f, map[string]any{"container_selector": "#feed", "record_selector": ".record", "text_selector": ".body", "author_selector": ".author", "time_selector": ".time", "page_size": float64(2), "page_token": second["next_page_token"]})
	if err != nil {
		t.Fatal(err)
	}
	page3 := third["records"].([]any)
	if len(page3) != 1 || page3[0].(map[string]any)["record_id"] != "record-5" || third["has_more"] != false {
		t.Fatalf("third page records/summary = %#v", third)
	}
}

func TestBrowserRecordReadRejectsMutationBetweenPages(t *testing.T) {
	f := newBrowserFixture(t, `<main id="feed"><article class="record" id="one">one</article><article class="record" id="two">two</article></main>`)
	first, err := readRecords(t, f, map[string]any{"container_selector": "#feed", "record_selector": ".record", "page_size": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	tab := f.b.sessions[f.session].tabs[f.tab]
	if err := f.b.eval(f.ctx, tab, "", `(()=>{const row=document.createElement('article');row.className='record';row.id='three';row.textContent='three';document.querySelector('#feed').append(row);return true})()`, nil); err != nil {
		t.Fatal(err)
	}
	_, err = readRecords(t, f, map[string]any{"container_selector": "#feed", "record_selector": ".record", "page_size": float64(1), "page_token": first["next_page_token"]})
	browserErrorCode(t, err, "stale_generation")
}

func TestBrowserRecordReadRejectsReplacedContainerBetweenPages(t *testing.T) {
	f := newBrowserFixture(t, `<main id="feed"><article class="record" id="one">one</article><article class="record" id="two">two</article></main>`)
	first, err := readRecords(t, f, map[string]any{"container_selector": "#feed", "record_selector": ".record", "page_size": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	tab := f.b.sessions[f.session].tabs[f.tab]
	if err := f.b.eval(f.ctx, tab, "", `(()=>{const old=document.querySelector('#feed'),replacement=old.cloneNode(true);old.replaceWith(replacement);return true})()`, nil); err != nil {
		t.Fatal(err)
	}
	_, err = readRecords(t, f, map[string]any{"container_selector": "#feed", "record_selector": ".record", "page_size": float64(1), "page_token": first["next_page_token"]})
	browserErrorCode(t, err, "stale_generation")
}

func TestBrowserRecordReadRejectsBadSelectorsAndIDs(t *testing.T) {
	f := newBrowserFixture(t, `<main id="feed"><article class="record">missing ID</article><article class="duplicate" id="dup">one</article><article class="duplicate" id="dup">two</article></main>`)
	for _, tc := range []struct {
		args map[string]any
		code string
	}{
		{map[string]any{"container_selector": "[", "record_selector": ".record"}, "invalid_selector"},
		{map[string]any{"container_selector": "#feed", "record_selector": "["}, "invalid_selector"},
		{map[string]any{"container_selector": "#feed", "record_selector": ".record"}, "unstable_record"},
		{map[string]any{"container_selector": "#feed", "record_selector": ".duplicate"}, "ambiguous_record"},
	} {
		_, err := readRecords(t, f, tc.args)
		browserErrorCode(t, err, tc.code)
	}
}

func TestBrowserRecordReadBoundsDOMTextAndAttachments(t *testing.T) {
	attachments := strings.Repeat(`<a href="https://files.example.test/file.txt?token=SECRET">file</a>`, 40)
	longText := strings.Repeat("x", 5000)
	body := `<main id="feed"><article class="record" id="one"><p class="body">` + longText + `</p>` + attachments + `</article>`
	for i := 2; i <= 2101; i++ {
		body += fmt.Sprintf(`<article class="record" id="record-%d">row</article>`, i)
	}
	body += `</main>`
	f := newBrowserFixture(t, body)
	result, err := readRecords(t, f, map[string]any{"container_selector": "#feed", "record_selector": ".record", "text_selector": ".body", "page_size": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	records := result["records"].([]any)
	if len(records) != 1 {
		t.Fatalf("bounded page length = %d", len(records))
	}
	record := records[0].(map[string]any)
	if len(record["text"].(string)) != 4096 || len(record["attachments"].([]any)) != 32 {
		t.Fatalf("record was not bounded: text=%d attachments=%d", len(record["text"].(string)), len(record["attachments"].([]any)))
	}
	if result["total_records"] != float64(2101) || result["truncated"] != true || !containsString(result["truncation_reasons"].([]any), "record_limit") || !containsString(result["truncation_reasons"].([]any), "text_limit") || !containsString(result["truncation_reasons"].([]any), "attachment_limit") {
		t.Fatalf("bounded collection metadata = %#v", result)
	}
}

func TestBrowserRecordReadRowCapEndsCursor(t *testing.T) {
	var body strings.Builder
	body.WriteString(`<main id="feed">`)
	for i := 1; i <= 2101; i++ {
		fmt.Fprintf(&body, `<article class="record" id="record-%d">row</article>`, i)
	}
	body.WriteString(`</main>`)
	f := newBrowserFixture(t, body.String())
	args := map[string]any{"container_selector": "#feed", "record_selector": ".record", "page_size": float64(100)}
	result, err := readRecords(t, f, args)
	if err != nil {
		t.Fatal(err)
	}
	for expectedOffset := 100; expectedOffset < 2000; expectedOffset += 100 {
		if result["has_more"] != true {
			t.Fatalf("cursor ended early at %d: %#v", expectedOffset, result)
		}
		token := result["next_page_token"].(string)
		parts := strings.Split(token, ":")
		if len(parts) != 2 {
			t.Fatalf("invalid page token %q", token)
		}
		gotOffset, err := strconv.Atoi(parts[1])
		if err != nil || gotOffset != expectedOffset {
			t.Fatalf("next offset = %q, want %d", parts[1], expectedOffset)
		}
		result, err = readRecords(t, f, map[string]any{"container_selector": "#feed", "record_selector": ".record", "page_size": float64(100), "page_token": token})
		if err != nil {
			t.Fatal(err)
		}
	}
	if result["has_more"] != false || result["next_page_token"] != "" || result["total_records"] != float64(2101) || result["truncated"] != true || !containsString(result["truncation_reasons"].([]any), "record_limit") {
		t.Fatalf("row cap terminal page = %#v", result)
	}
}

func TestBrowserRecordReadShrinksPageToResponseBudget(t *testing.T) {
	longPath := strings.Repeat("p", 650)
	var body strings.Builder
	body.WriteString(`<main id="feed">`)
	for i := 1; i <= 4; i++ {
		fmt.Fprintf(&body, `<article class="record" id="record-%d">`, i)
		for j := 0; j < 32; j++ {
			fmt.Fprintf(&body, `<a href="https://files.example.test/%s-%d-%d">file</a>`, longPath, i, j)
		}
		body.WriteString(`</article>`)
	}
	body.WriteString(`</main>`)
	f := newBrowserFixture(t, body.String())
	first, err := readRecords(t, f, map[string]any{"container_selector": "#feed", "record_selector": ".record", "page_size": float64(4)})
	if err != nil {
		t.Fatal(err)
	}
	records := first["records"].([]any)
	if len(records) < 1 || len(records) >= 4 || first["has_more"] != true || !containsString(first["truncation_reasons"].([]any), "response_budget") {
		t.Fatalf("page was not shrunk to fit payload budget: %#v", first)
	}
	nextToken := first["next_page_token"].(string)
	parts := strings.Split(nextToken, ":")
	nextOffset, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || nextOffset != len(records) {
		t.Fatalf("cursor skipped rows after shrink: token=%q records=%d", nextToken, len(records))
	}
	second, err := readRecords(t, f, map[string]any{"container_selector": "#feed", "record_selector": ".record", "page_size": float64(1), "page_token": nextToken})
	if err != nil {
		t.Fatal(err)
	}
	wantNextID := fmt.Sprintf("record-%d", len(records)+1)
	if second["records"].([]any)[0].(map[string]any)["record_id"] != wantNextID {
		t.Fatalf("shrunken page cursor skipped or repeated records: first=%#v next=%#v", first, second)
	}
}

func TestBrowserRecordReadFailsWhenSingleRecordExceedsBudget(t *testing.T) {
	longPath := strings.Repeat("q", 2048)
	var body strings.Builder
	body.WriteString(`<main id="feed"><article class="record" id="only">`)
	for i := 0; i < 32; i++ {
		fmt.Fprintf(&body, `<a href="https://files.example.test/%s-%d">file</a>`, longPath, i)
	}
	body.WriteString(`</article></main>`)
	f := newBrowserFixture(t, body.String())
	_, err := readRecords(t, f, map[string]any{"container_selector": "#feed", "record_selector": ".record", "page_size": float64(1)})
	browserErrorCode(t, err, "response_too_large")
}

func containsString(values []any, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
