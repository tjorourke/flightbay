package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestStartMultipleContextsAndPrivateKubeconfigUpload(t *testing.T) {
	e := newStart(t)
	path := writeKubeconfig(t, "https://127.0.0.1:1")
	body, _ := json.Marshal(LiveRequest{Kubeconfigs: []string{path}, Contexts: []string{"fake", "other", "fake"}})
	if resp, b := e.do(t, "POST", "/api/start/live", string(body), true); resp.StatusCode != 200 {
		t.Fatalf("live: %d %s", resp.StatusCode, b)
	}
	a := e.wantLaunch(t)
	if len(a.Live.Contexts) != 2 || a.Live.Contexts[1] != "other" {
		t.Fatalf("lost multiple selections: %+v", a.Live)
	}
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	config, _ := os.ReadFile(path)
	part, _ := writer.CreateFormFile("files", "first.yaml")
	_, _ = part.Write(config)
	part, _ = writer.CreateFormFile("files", "second.yaml")
	_, _ = part.Write([]byte(strings.ReplaceAll(string(config), "fake", "second")))
	_ = writer.Close()
	req, _ := http.NewRequest("POST", e.url+"/api/start/kubeconfigs", &buf)
	req.Header.Set("Authorization", "Bearer "+startTok)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("upload: %d %s", resp.StatusCode, b)
	}
	var uploaded struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(b, &uploaded); err != nil {
		t.Fatal(err)
	}
	if len(uploaded.Files) != 2 || bytes.Contains(b, []byte("token:")) {
		t.Fatal("upload lost files or exposed credential data")
	}
	for _, file := range uploaded.Files {
		st, err := os.Stat(file)
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatalf("config permissions: %v", err)
		}
	}
}
