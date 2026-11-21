package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCurseForgeBatchFillsMissingIDsFromOfficialAPI(t *testing.T) {
	t.Setenv("CURSEFORGE_API_KEY", "test-key")
	previous := client.Transport
	t.Cleanup(func() { client.Transport = previous })
	client.Transport = curseTestTransport(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body.Close()
		var id int
		switch req.URL.Host {
		case "mod.mcimirror.top":
			if string(body) != `{"fileIds":[123,456]}` || req.Header.Get("X-Api-Key") != "" {
				t.Errorf("invalid mirror request")
			}
			id = 123
		case "api.curseforge.com":
			if string(body) != `{"fileIds":[456]}` || req.Header.Get("X-Api-Key") != "test-key" {
				t.Errorf("invalid official request")
			}
			id = 456
		default:
			return nil, fmt.Errorf("unexpected request to %s", req.URL.Host)
		}
		data := fmt.Appendf(nil, `{"data":[{"id":%d,"modId":42,"fileName":"mod.jar","fileLength":1,"hashes":[{"algo":1,"value":"a5be2cc0560d595c39d5324b5a50a73c8c5f819c"}]}]}`, id)
		return &http.Response{StatusCode: http.StatusOK, Request: req, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	rows, err := curseGetFiles(t.Context(), []int{123, 456})
	var ids []int
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	if err != nil || !slices.Equal(ids, []int{123, 456}) {
		t.Fatalf("got %v, %v", ids, err)
	}
}

func TestCurseForgePublicFallbackWhenMCIMIsUnavailable(t *testing.T) {
	t.Setenv("CURSEFORGE_API_KEY", "")
	var jar bytes.Buffer
	z := zip.NewWriter(&jar)
	entry, err := z.Create("META-INF/MANIFEST.MF")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(entry, "Manifest-Version: 1.0\n"); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	previous := client.Transport
	t.Cleanup(func() { client.Transport = previous })
	var corrupt bool
	client.Transport = curseTestTransport(func(req *http.Request) (*http.Response, error) {
		var body []byte
		switch req.URL.Host + req.URL.Path {
		case "mod.mcimirror.top/curseforge/v1/mods/1418069/files/9379262":
			return nil, context.DeadlineExceeded
		case "api.cfwidget.com/1418069":
			body = fmt.Appendf(nil, `{"id":1418069,"game":"minecraft","type":"Mods","files":[{"id":9379262,"name":"hats-1.21.1-3.0.0.jar","filesize":%d}]}`, jar.Len())
		case "mediafilez.forgecdn.net/files/9379/262/hats-1.21.1-3.0.0.jar":
			body = jar.Bytes()
			if corrupt {
				body = make([]byte, jar.Len())
			}
		default:
			t.Errorf("unexpected request %s", req.URL)
			return nil, context.DeadlineExceeded
		}
		return &http.Response{StatusCode: http.StatusOK, Request: req, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	row, err := curseGetFile(t.Context(), 1418069, 9379262, "1.21.1")
	if err != nil {
		t.Fatal(err)
	}
	game := t.TempDir()
	item, err := cursePackTransfer(t.Context(), game, t.TempDir(), row)
	if err != nil {
		t.Fatal(err)
	}
	if err := item.ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(game, "mods", "hats-1.21.1-3.0.0.jar")); err != nil || !bytes.Equal(data, jar.Bytes()) {
		t.Fatalf("verified archive was not installed: %v", err)
	}
	corrupt = true
	item.target = filepath.Join(game, "invalid.jar")
	if err := item.ensure(t.Context()); err == nil || regularFile(item.target) {
		t.Fatalf("same-size corrupt archive was accepted: %v", err)
	}
}
