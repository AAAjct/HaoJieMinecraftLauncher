package engine

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChineseModNames(t *testing.T) {
	field := func(tag byte, value []byte) []byte {
		return append(binary.AppendUvarint([]byte{tag}, uint64(len(value))), value...)
	}
	record := func(name, slug string) []byte {
		data := append([]byte{8, 1}, field(18, []byte(name))...)
		return field(10, append(data, field(34, []byte(slug))...))
	}
	var data bytes.Buffer
	writer := gzip.NewWriter(&data)
	_, _ = writer.Write(append(record("钠 (Sodium)", "sodium"), record("钠扩展 (Sodium Extra)", "sodium-extra")...))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	names, err := decodeModNames(data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if names.Name("sodium") != "钠" || names.SearchSlug(" 钠 ") != "sodium" ||
		names.SearchSlug("扩展") != "sodium-extra" || names.SearchSlug("sodium") != "" {
		t.Fatalf("unexpected names: %+v", names)
	}
}

func TestLiveChineseModSearch(t *testing.T) {
	if os.Getenv("HJML_INTEGRATION") != "1" {
		t.Skip("set HJML_INTEGRATION=1 to fetch and search the upstream index")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	e := Engine{DataDir: t.TempDir()}
	names, err := e.LoadModNames(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]string{"钠": "sodium", "玉": "jade", "鼠标手势": "mouse-tweaks"} {
		slug := names.SearchSlug(query)
		if slug != want {
			t.Fatalf("%q => %q, want %q", query, slug, want)
		}
		hits, _, err := Search(ctx, "mod", strings.ReplaceAll(slug, "-", " "), Instance{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, hit := range hits {
			found = found || hit.Slug == slug
		}
		if !found {
			t.Fatalf("search for %q did not return %s", query, slug)
		}
	}
	if _, err := os.Stat(filepath.Join(e.DataDir, ".hjml-cache", "mcmod.buf")); err != nil {
		t.Fatal(err)
	}
}
