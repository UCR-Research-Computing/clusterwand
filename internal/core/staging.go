package core

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/UCR-Research-Computing/clusterwand/internal/backend"
	"github.com/UCR-Research-Computing/clusterwand/internal/config"
	"github.com/UCR-Research-Computing/clusterwand/internal/policy"
)

// Bounds on local-SSH input staging in job_submit so accidental dataset uploads
// are caught before streaming over stdin (multi-GB data belongs in $SCRATCH via rsync/scp).
const (
	MaxSubmitInputs          = 32
	MaxSubmitInputBytes      = 64 << 20  // 64 MiB compressed/raw inputs
	MaxSubmitUnpackedBytes   = 256 << 20 // 256 MiB after unpacking .tar.gz/.tgz
	MaxSubmitUnpackedEntries = 4096
)

// InputSpec describes one file or archive staged into ~/clusterwand-jobs/<run>/inputs/
// over SSH when job_submit_confirm runs.
type InputSpec struct {
	LocalPath     string `json:"local_path,omitempty" jsonschema:"local regular file to stage into inputs/ (symlinks and credential-like files are refused)"`
	Path          string `json:"path,omitempty" jsonschema:"relative destination path inside inputs/, e.g. config.yaml or data/in.csv (defaults to basename of local_path)"`
	Content       string `json:"content,omitempty" jsonschema:"inline UTF-8 text content when not using local_path"`
	ContentBase64 string `json:"content_base64,omitempty" jsonschema:"base64-encoded bytes when not using local_path"`
}

// StagedFile summarizes one validated file that will land under inputs/.
type StagedFile struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type stagedEntry struct {
	rel  string
	data []byte
}

func (s *Service) stagedTarPath(planHash string) string {
	return filepath.Join(filepath.Dir(s.statePath()), "staged", planHash+".tar.gz")
}

// cleanStagedArchives deletes any staged .tar.gz snapshots not referenced by a
// live pending token.
func (s *Service) cleanStagedArchives(st *a1State) {
	dir := filepath.Join(filepath.Dir(s.statePath()), "staged")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, p := range st.Pending {
		if p.InputsSHA256 != "" {
			keep[p.Hash+".tar.gz"] = true
		}
	}
	for _, e := range ents {
		if !keep[e.Name()] {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func checkLocalSourcePath(raw string) (string, error) {
	p := config.Expand(strings.TrimSpace(raw))
	if p == "" {
		return "", errors.New("local_path is empty")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	// Refuse hidden or credential-like basenames or sensitive local config dirs.
	if hiddenOrSensitive(filepath.Base(abs)) {
		return "", fmt.Errorf("%w: refusing to stage hidden or credential-like file %q", policy.ErrDenied, filepath.Base(abs))
	}
	slashed := filepath.ToSlash(abs)
	for _, bad := range []string{"/.ssh/", "/.aws/", "/.gnupg/", "/.kube/", "/.config/gcloud/"} {
		if strings.Contains(slashed, bad) {
			return "", fmt.Errorf("%w: refusing to stage file from sensitive directory %s", policy.ErrDenied, strings.Trim(bad, "/"))
		}
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("staging %s: %w", raw, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: refusing to stage symlink %s (pass the real regular file)", policy.ErrDenied, raw)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("staging %s: not a regular file", raw)
	}
	if fi.Size() > MaxSubmitInputBytes {
		return "", fmt.Errorf("staging %s: %d bytes exceeds the %d byte staging cap (use rsync/scp into $SCRATCH for large datasets)", raw, fi.Size(), MaxSubmitInputBytes)
	}
	return abs, nil
}

func isTarGzPath(p string) bool {
	low := strings.ToLower(p)
	return strings.HasSuffix(low, ".tar.gz") || strings.HasSuffix(low, ".tgz")
}

// prepareStagedInputs reads and validates every InputSpec on the local machine
// (zero cluster writes), optionally unpacking .tar.gz/.tgz archives in Go so
// unsafe entries or zip bombs are rejected before confirmation, and returns the
// deterministic .tar.gz payload, its SHA-256 hex, total unpacked bytes, and file list.
func prepareStagedInputs(specs []InputSpec, unpack bool) ([]byte, string, int64, []StagedFile, error) {
	if len(specs) == 0 {
		return nil, "", 0, nil, nil
	}
	if len(specs) > MaxSubmitInputs {
		return nil, "", 0, nil, fmt.Errorf("at most %d staged inputs per job_submit, got %d", MaxSubmitInputs, len(specs))
	}
	var rawTotal int64
	var entries []stagedEntry
	seen := map[string]bool{}
	addEntry := func(rel string, data []byte) error {
		rel = strings.TrimPrefix(rel, "./")
		if err := backend.ValidRelPath(rel); err != nil {
			return fmt.Errorf("%w: staged input path %q: %v", policy.ErrDenied, rel, err)
		}
		if hiddenRel(rel) {
			return fmt.Errorf("%w: staged input %q is hidden or looks like a credential", policy.ErrDenied, rel)
		}
		if seen[rel] {
			return fmt.Errorf("duplicate staged input path %q", rel)
		}
		seen[rel] = true
		entries = append(entries, stagedEntry{rel: rel, data: data})
		return nil
	}

	for idx, sp := range specs {
		sources := 0
		if strings.TrimSpace(sp.LocalPath) != "" {
			sources++
		}
		if sp.Content != "" {
			sources++
		}
		if sp.ContentBase64 != "" {
			sources++
		}
		// Allow empty inline Content="" when Path is set and neither LocalPath nor Base64 is set.
		if sources == 0 && strings.TrimSpace(sp.Path) != "" && strings.TrimSpace(sp.LocalPath) == "" && sp.ContentBase64 == "" {
			sources = 1
		}
		if sources != 1 {
			return nil, "", 0, nil, fmt.Errorf("inputs[%d]: set exactly one of local_path, content, or content_base64", idx)
		}
		var data []byte
		rel := strings.TrimSpace(sp.Path)
		switch {
		case strings.TrimSpace(sp.LocalPath) != "":
			abs, err := checkLocalSourcePath(sp.LocalPath)
			if err != nil {
				return nil, "", 0, nil, err
			}
			b, err := os.ReadFile(abs)
			if err != nil {
				return nil, "", 0, nil, err
			}
			data = b
			if rel == "" {
				rel = filepath.Base(abs)
			}
		case sp.ContentBase64 != "":
			if rel == "" {
				return nil, "", 0, nil, fmt.Errorf("inputs[%d]: path is required with content_base64", idx)
			}
			b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sp.ContentBase64))
			if err != nil {
				return nil, "", 0, nil, fmt.Errorf("inputs[%d]: invalid base64: %w", idx, err)
			}
			data = b
		default:
			if rel == "" {
				return nil, "", 0, nil, fmt.Errorf("inputs[%d]: path is required with content", idx)
			}
			data = []byte(sp.Content)
		}
		rawTotal += int64(len(data))
		if rawTotal > MaxSubmitInputBytes {
			return nil, "", 0, nil, fmt.Errorf("staged inputs total %d bytes, over the %d byte cap", rawTotal, MaxSubmitInputBytes)
		}
		if unpack && isTarGzPath(rel) {
			prefix := ""
			if dir := path.Dir(rel); dir != "." && dir != "" {
				prefix = dir + "/"
			}
			gz, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				return nil, "", 0, nil, fmt.Errorf("unpacking %s: %w", rel, err)
			}
			tr := tar.NewReader(gz)
			for {
				hdr, err := tr.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					_ = gz.Close()
					return nil, "", 0, nil, fmt.Errorf("unpacking %s: %w", rel, err)
				}
				entryName := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
				if entryName == "." || entryName == "" {
					continue
				}
				if path.IsAbs(entryName) || entryName == ".." || strings.HasPrefix(entryName, "../") {
					_ = gz.Close()
					return nil, "", 0, nil, fmt.Errorf("%w: archive %s contains unsafe path %q", policy.ErrDenied, rel, hdr.Name)
				}
				switch hdr.Typeflag {
				case tar.TypeDir:
					continue
				case tar.TypeReg:
					if hdr.Size > MaxSubmitUnpackedBytes {
						_ = gz.Close()
						return nil, "", 0, nil, fmt.Errorf("archive %s entry %q exceeds unpacked size limit", rel, hdr.Name)
					}
					eb, err := io.ReadAll(io.LimitReader(tr, MaxSubmitUnpackedBytes+1))
					if err != nil {
						_ = gz.Close()
						return nil, "", 0, nil, fmt.Errorf("unpacking %s: %w", rel, err)
					}
					if int64(len(eb)) > MaxSubmitUnpackedBytes {
						_ = gz.Close()
						return nil, "", 0, nil, fmt.Errorf("archive %s entry %q exceeds unpacked size limit", rel, hdr.Name)
					}
					if err := addEntry(prefix+entryName, eb); err != nil {
						_ = gz.Close()
						return nil, "", 0, nil, err
					}
				default:
					_ = gz.Close()
					return nil, "", 0, nil, fmt.Errorf("%w: archive %s contains non-regular entry %q (symlinks/devices are refused)", policy.ErrDenied, rel, hdr.Name)
				}
				if len(entries) > MaxSubmitUnpackedEntries {
					_ = gz.Close()
					return nil, "", 0, nil, fmt.Errorf("archive %s unpacks to more than %d files", rel, MaxSubmitUnpackedEntries)
				}
			}
			_ = gz.Close()
			continue
		}
		if err := addEntry(rel, data); err != nil {
			return nil, "", 0, nil, err
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	var unpackedTotal int64
	var summary []StagedFile
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		unpackedTotal += int64(len(e.data))
		if unpackedTotal > MaxSubmitUnpackedBytes {
			return nil, "", 0, nil, fmt.Errorf("unpacked staged inputs total %d bytes, over the %d byte limit", unpackedTotal, MaxSubmitUnpackedBytes)
		}
		hdr := &tar.Header{
			Name:     e.rel,
			Mode:     0o644,
			Size:     int64(len(e.data)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, "", 0, nil, err
		}
		if _, err := tw.Write(e.data); err != nil {
			return nil, "", 0, nil, err
		}
		sum := sha256.Sum256(e.data)
		summary = append(summary, StagedFile{
			Path:   "inputs/" + e.rel,
			Bytes:  int64(len(e.data)),
			SHA256: hex.EncodeToString(sum[:8]),
		})
	}
	if err := tw.Close(); err != nil {
		return nil, "", 0, nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, "", 0, nil, err
	}
	tarGz := buf.Bytes()
	archiveSum := sha256.Sum256(tarGz)
	return tarGz, hex.EncodeToString(archiveSum[:]), unpackedTotal, summary, nil
}
