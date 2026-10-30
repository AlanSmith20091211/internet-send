package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, pin string) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(Config{Dir: dir, PIN: pin, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(func() { s.Close(); ts.Close() })
	return ts, dir
}

// sse is a minimal EventSource client for tests.
type sse struct {
	resp   *http.Response
	events chan [2]string
}

func connect(t *testing.T, ts *httptest.Server, id, name string) *sse {
	t.Helper()
	resp, err := http.Get(ts.URL + "/api/events?device=" + id + "&name=" + url.QueryEscape(name))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("events: status %d", resp.StatusCode)
	}
	c := &sse{resp: resp, events: make(chan [2]string, 32)}
	go func() {
		sc := bufio.NewScanner(resp.Body)
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = line[7:]
			case strings.HasPrefix(line, "data: "):
				c.events <- [2]string{name, line[6:]}
			}
		}
		close(c.events)
	}()
	t.Cleanup(func() { resp.Body.Close() })
	return c
}

func (c *sse) next(t *testing.T, want string) string {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-c.events:
			if !ok {
				t.Fatalf("stream closed waiting for %q", want)
			}
			if ev[0] == want {
				return ev[1]
			}
		case <-timeout:
			t.Fatalf("timeout waiting for %q", want)
		}
	}
}

func TestUploadDownloadAndVisibility(t *testing.T) {
	ts, dir := newTestServer(t, "")
	alice := connect(t, ts, "alice-0001", "Alice")
	alice.next(t, "hello")
	bob := connect(t, ts, "bob-00001", "Bob")
	bob.next(t, "hello")
	carol := connect(t, ts, "carol-0001", "Carol")
	carol.next(t, "hello")

	content := bytes.Repeat([]byte("hello "), 10000)
	resp, err := http.Post(ts.URL+"/api/upload?device=alice-0001&to=bob-00001&name="+url.QueryEscape("../照片.jpg"),
		"application/octet-stream", bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	var it Item
	json.NewDecoder(resp.Body).Decode(&it)
	resp.Body.Close()
	if resp.StatusCode != 200 || it.Name != "照片.jpg" || it.Size != int64(len(content)) {
		t.Fatalf("upload: status %d item %+v", resp.StatusCode, it)
	}
	if _, err := os.Stat(filepath.Join(dir, "照片.jpg")); err != nil {
		t.Fatalf("file not stored in dir: %v", err)
	}

	// Bob is the recipient and gets an event.
	var got Item
	json.Unmarshal([]byte(bob.next(t, "item")), &got)
	if got.ID != it.ID || got.FromName != "Alice" {
		t.Fatalf("bob got %+v", got)
	}

	// Bob can download (including a Range request), Carol cannot.
	req, _ := http.NewRequest("GET", ts.URL+"/api/items/"+it.ID+"/file?device=bob-00001", nil)
	req.Header.Set("Range", "bytes=0-4")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(body) != "hello" {
		t.Fatalf("range download: %d %q", resp.StatusCode, body)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("content-disposition %q", cd)
	}
	resp, _ = http.Get(ts.URL + "/api/items/" + it.ID + "/file?device=carol-0001")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("carol should not see private file, got %d", resp.StatusCode)
	}

	// A second upload with the same name must not overwrite the first.
	resp, _ = http.Post(ts.URL+"/api/upload?device=alice-0001&name=照片.jpg", "application/octet-stream", strings.NewReader("x"))
	resp.Body.Close()
	if _, err := os.Stat(filepath.Join(dir, "照片 (1).jpg")); err != nil {
		t.Fatalf("expected deduplicated name: %v", err)
	}
	// ...and since it went to everyone, Carol hears about it.
	carol.next(t, "item")
}

func TestText(t *testing.T) {
	ts, _ := newTestServer(t, "")
	a := connect(t, ts, "aaaaaaaa", "A")
	a.next(t, "hello")
	b := connect(t, ts, "bbbbbbbb", "B")
	b.next(t, "hello")

	resp, _ := http.Post(ts.URL+"/api/text", "application/json",
		strings.NewReader(`{"device":"aaaaaaaa","text":"https://example.com"}`))
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("text status %d", resp.StatusCode)
	}
	var it Item
	json.Unmarshal([]byte(b.next(t, "item")), &it)
	if it.Text != "https://example.com" {
		t.Fatalf("got %+v", it)
	}

	// Unknown sender is rejected.
	resp, _ = http.Post(ts.URL+"/api/text", "application/json", strings.NewReader(`{"device":"zzzzzzzz","text":"x"}`))
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("unknown device status %d", resp.StatusCode)
	}
}

func TestPIN(t *testing.T) {
	ts, _ := newTestServer(t, "123456")

	resp, _ := http.Get(ts.URL + "/api/events?device=aaaaaaaa")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	resp, _ = http.Post(ts.URL+"/api/login", "application/json", strings.NewReader(`{"pin":"000000"}`))
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong pin: %d", resp.StatusCode)
	}

	resp, _ = http.Post(ts.URL+"/api/login", "application/json", strings.NewReader(`{"pin":"123456"}`))
	resp.Body.Close()
	cookies := resp.Cookies()
	if resp.StatusCode != 200 || len(cookies) == 0 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/info", nil)
	req.AddCookie(cookies[0])
	resp, _ = http.DefaultClient.Do(req)
	var info map[string]any
	json.NewDecoder(resp.Body).Decode(&info)
	resp.Body.Close()
	if info["authorized"] != true {
		t.Fatalf("info after login: %v", info)
	}

	// Brute force gets locked out.
	for i := 0; i < maxFailures; i++ {
		r, _ := http.Post(ts.URL+"/api/login", "application/json", strings.NewReader(`{"pin":"999999"}`))
		r.Body.Close()
	}
	resp, _ = http.Post(ts.URL+"/api/login", "application/json", strings.NewReader(`{"pin":"123456"}`))
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected lockout, got %d", resp.StatusCode)
	}
}

func TestStaticUI(t *testing.T) {
	ts, _ := newTestServer(t, "")
	resp, _ := http.Get(ts.URL + "/")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "Internet Send") {
		t.Fatalf("index: %d", resp.StatusCode)
	}
	resp, _ = http.Get(ts.URL + "/api/nope")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown api: %d", resp.StatusCode)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"a.txt":              "a.txt",
		"../../etc/passwd":   "passwd",
		`C:\Users\x\doc.pdf`: "doc.pdf",
		"":                   "file",
		"..":                 "file",
		"bad:name?.txt":      "bad_name_.txt",
		"CON.txt":            "_CON.txt",
		"trailing. ":         "trailing",
		"中文 文件.docx":         "中文 文件.docx",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("很", 100) + ".zip"
	if got := sanitizeName(long); len(got) > 200 || !strings.HasSuffix(got, ".zip") {
		t.Errorf("long name not truncated correctly: %d bytes %q", len(got), got)
	}
}
