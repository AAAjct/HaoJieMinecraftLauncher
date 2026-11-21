package engine

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	modNamesRev  = "676a752d304165e8c5fd3729841f1582bbf4b795"
	modNamesPath = "/Plain%20Craft%20Launcher%202/Resources/mcmod.buf"
	modNamesBlob = "https://api.github.com/repos/PCL-Community/PCL-CE/git/blobs/6fc9249fcac3e1d6d6717557aaf03d0e20d22bea"
	modNamesSHA  = "b72c3ed18407483d22767859602f8ecce45770fd928ce12a45da2a433e479320"
)

type ModNames struct {
	names map[string]string
}

func (e Engine) LoadModNames(ctx context.Context) (*ModNames, error) {
	dir := e.cacheDir()
	path := filepath.Join(dir, "mcmod.buf")
	data, err := os.ReadFile(path)
	if err == nil && validModNames(data) {
		return decodeModNames(data)
	}
	source := func(address string, jsonBlob bool) catalogSource[[]byte] {
		return func(ctx context.Context) ([]byte, error) {
			res, err := get(ctx, address)
			if err != nil {
				return nil, err
			}
			defer res.Body.Close()
			var data []byte
			body := io.LimitReader(res.Body, 2<<20)
			if jsonBlob {
				var blob struct{ Content string }
				if err := json.NewDecoder(body).Decode(&blob); err != nil {
					return nil, err
				}
				data, err = base64.StdEncoding.DecodeString(blob.Content)
			} else {
				data, err = io.ReadAll(body)
			}
			if err != nil {
				return nil, err
			}
			if !validModNames(data) {
				return nil, fmt.Errorf("模组中文索引的校验值不匹配")
			}
			return data, nil
		}
	}
	data, err = firstCatalog(ctx,
		source("https://raw.githubusercontent.com/PCL-Community/PCL-CE/"+modNamesRev+modNamesPath, false),
		source(modNamesBlob, true),
	)
	if err != nil {
		return nil, err
	}
	names, err := decodeModNames(data)
	if err == nil && os.MkdirAll(dir, 0700) == nil {
		_ = writeFile(path, data)
	}
	return names, err
}

func validModNames(data []byte) bool {
	return fmt.Sprintf("%x", sha256.Sum256(data)) == modNamesSHA
}

func decodeModNames(data []byte) (*ModNames, error) {
	z, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer z.Close()
	data, err = io.ReadAll(io.LimitReader(z, 8<<20))
	if err != nil {
		return nil, err
	}
	names := &ModNames{names: make(map[string]string)}
	// mcmod.buf is a gzip-compressed protobuf list of MCMOD entries.
	err = readNameFields(bytes.NewReader(data), func(field uint64, record []byte) error {
		if field != 1 {
			return nil
		}
		var title, curseforge, modrinth string
		err := readNameFields(bytes.NewReader(record), func(field uint64, value []byte) error {
			switch field {
			case 2:
				title = string(value)
			case 3:
				curseforge = string(value)
			case 4:
				modrinth = string(value)
			}
			return nil
		})
		if err != nil {
			return err
		}
		name, _, _ := strings.Cut(title, " (")
		slug := modrinth
		if slug == "" {
			slug = curseforge
		}
		if _, exists := names.names[slug]; slug != "" && !exists && strings.ContainsFunc(name, isHan) {
			names.names[slug] = strings.TrimSpace(name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return names, nil
}

func readNameFields(r *bytes.Reader, read func(uint64, []byte) error) error {
	for r.Len() > 0 {
		tag, err := binary.ReadUvarint(r)
		if err != nil {
			return err
		}
		switch tag & 7 {
		case 0:
			if _, err := binary.ReadUvarint(r); err != nil {
				return err
			}
		case 2:
			size, err := binary.ReadUvarint(r)
			if err != nil || size > uint64(r.Len()) {
				return io.ErrUnexpectedEOF
			}
			value := make([]byte, int(size))
			if _, err := io.ReadFull(r, value); err != nil {
				return err
			}
			if err := read(tag>>3, value); err != nil {
				return err
			}
		default:
			return fmt.Errorf("模组中文索引字段无效")
		}
	}
	return nil
}

func isHan(r rune) bool { return unicode.Is(unicode.Han, r) }

func (m *ModNames) Name(slug string) string {
	if m == nil {
		return ""
	}
	return m.names[strings.ReplaceAll(strings.ToLower(slug), "_", "-")]
}

func (m *ModNames) SearchSlug(query string) string {
	if m == nil || !strings.ContainsFunc(query, isHan) {
		return ""
	}
	normalize := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return unicode.ToLower(r)
			}
			return -1
		}, s)
	}
	query = normalize(query)
	best, bestRank, bestLength := "", 0, 0
	for slug, name := range m.names {
		for alias := range strings.SplitSeq(name, "/") {
			alias = normalize(alias)
			rank := 0
			switch {
			case alias == query:
				rank = 3
			case strings.HasPrefix(alias, query):
				rank = 2
			case strings.Contains(alias, query):
				rank = 1
			}
			length := len(alias)
			if rank > 0 && (rank > bestRank || rank == bestRank &&
				(length < bestLength || length == bestLength &&
					(len(slug) < len(best) || len(slug) == len(best) && slug < best))) {
				best, bestRank, bestLength = slug, rank, length
			}
		}
	}
	return best
}
