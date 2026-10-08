package dscpkg

import (
	"archive/zip"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const maxArchiveSize = int64(4 << 30)
const maxExpandedSize = uint64(8 << 30)

func (c *Client) installPackage(repo *repository, resolved packageResolution) (bool, error) {
	infoURL, err := packageDocumentURL(repo.discovery.Packages, resolved.Package, resolved.PackageVersion)
	if err != nil {
		return false, err
	}
	info, err := c.getDocument(infoURL)
	if err != nil {
		return false, err
	}
	if err := c.verifyDocument(repo, namespace(resolved.Package), infoURL, info); err != nil {
		return false, fmt.Errorf("package descriptor %s signature check: %w", infoURL, err)
	}
	var descriptor PackageDescriptor
	if err := json.Unmarshal(info.body, &descriptor); err != nil {
		return false, fmt.Errorf("parse package descriptor %s: %w", infoURL, err)
	}
	archive, exists := descriptor.Archives[c.Platform]
	if !exists {
		return false, fmt.Errorf("package %s %s has no archive for platform %s", resolved.Package, resolved.PackageVersion, c.Platform)
	}
	if strings.TrimSpace(archive.URL) == "" {
		return false, errors.New("package archive descriptor has no URL")
	}
	if len(archive.Hashes) == 0 {
		return false, errors.New("package archive descriptor has no hashes")
	}
	for _, entry := range archive.Hashes {
		if _, _, err := splitArchiveHash(entry); err != nil {
			return false, err
		}
	}
	archiveURL, err := resolveURL(infoURL, archive.URL)
	if err != nil {
		return false, err
	}
	parent := filepath.Dir(resolved.Path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return false, err
	}
	if info, err := os.Stat(resolved.Path); err == nil && info.IsDir() {
		return false, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	tmp, err := os.CreateTemp("", "dscpkg-archive-*.zip")
	if err != nil {
		return false, err
	}
	archiveTemp := tmp.Name()
	defer os.Remove(archiveTemp)
	request, err := http.NewRequest(http.MethodGet, archiveURL, nil)
	if err != nil {
		tmp.Close()
		return false, err
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		tmp.Close()
		return false, fmt.Errorf("download archive %s: %w", archiveURL, err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		tmp.Close()
		return false, fmt.Errorf("download archive %s: HTTP %s", archiveURL, response.Status)
	}
	writers := make(map[string]hash.Hash)
	for _, entry := range archive.Hashes {
		algorithm, _, _ := splitArchiveHash(entry)
		writers[algorithm] = hashFor(algorithm)
	}
	multi := []io.Writer{tmp}
	for _, hasher := range writers {
		multi = append(multi, hasher)
	}
	written, copyErr := io.Copy(io.MultiWriter(multi...), io.LimitReader(response.Body, maxArchiveSize+1))
	bodyCloseErr := response.Body.Close()
	if copyErr != nil {
		tmp.Close()
		return false, copyErr
	}
	if bodyCloseErr != nil {
		tmp.Close()
		return false, bodyCloseErr
	}
	if written > maxArchiveSize {
		tmp.Close()
		return false, fmt.Errorf("archive exceeds %d-byte size limit", maxArchiveSize)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	matched := false
	for _, entry := range archive.Hashes {
		algorithm, expected, _ := splitArchiveHash(entry)
		if hex.EncodeToString(writers[algorithm].Sum(nil)) == expected {
			matched = true
		}
	}
	if !matched {
		return false, fmt.Errorf("archive hash mismatch for %s %s (%s)", resolved.Package, resolved.PackageVersion, c.Platform)
	}

	stage, err := os.MkdirTemp(parent, ".dscpkg-install-*")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(stage)
	if err := extractZip(archiveTemp, stage); err != nil {
		return false, fmt.Errorf("extract package archive: %w", err)
	}
	if err := os.Rename(stage, resolved.Path); err != nil {
		if info, statErr := os.Stat(resolved.Path); statErr == nil && info.IsDir() {
			return false, nil
		}
		return false, fmt.Errorf("atomically install package: %w", err)
	}
	return true, nil
}

func splitArchiveHash(value string) (string, string, error) {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(value)), ":", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "", "", fmt.Errorf("invalid archive hash %q; expected algorithm:hex", value)
	}
	expectedLength := map[string]int{"sha1": 40, "sha256": 64, "sha384": 96, "sha512": 128}
	if size, ok := expectedLength[parts[0]]; !ok || len(parts[1]) != size {
		return "", "", fmt.Errorf("unsupported or malformed archive hash %q", value)
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		return "", "", fmt.Errorf("invalid archive hash %q: %w", value, err)
	}
	return parts[0], parts[1], nil
}

func hashFor(algorithm string) hash.Hash {
	switch algorithm {
	case "sha1":
		return sha1.New()
	case "sha256":
		return sha256.New()
	case "sha384":
		return sha512.New384()
	case "sha512":
		return sha512.New()
	default:
		return nil
	}
}

func extractZip(archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	var expanded uint64
	seen := make(map[string]bool, len(reader.File))
	root, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	for _, file := range reader.File {
		name := file.Name
		localName := filepath.FromSlash(name)
		if strings.Contains(name, `\`) || !filepath.IsLocal(localName) {
			return fmt.Errorf("unsafe archive path %q", name)
		}
		clean := filepath.Clean(localName)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe archive path %q", name)
		}
		target := filepath.Join(root, clean)
		relative, err := filepath.Rel(root, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("archive path escapes extraction directory: %q", name)
		}
		key := strings.ToLower(clean)
		if seen[key] {
			return fmt.Errorf("duplicate archive path %q", name)
		}
		seen[key] = true
		mode := file.Mode()
		if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
			return fmt.Errorf("unsupported file type in archive entry %q", name)
		}
		if file.UncompressedSize64 > maxExpandedSize-expanded {
			return fmt.Errorf("archive exceeds expanded size limit")
		}
		expanded += file.UncompressedSize64
		if mode.IsDir() || strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := file.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, safeFileMode(mode))
		if err != nil {
			in.Close()
			return err
		}
		n, copyErr := io.CopyN(out, in, int64(file.UncompressedSize64)+1)
		inCloseErr := in.Close()
		outCloseErr := out.Close()
		if copyErr != nil && copyErr != io.EOF {
			return copyErr
		}
		if n != int64(file.UncompressedSize64) {
			return fmt.Errorf("archive entry %q size mismatch", name)
		}
		if inCloseErr != nil {
			return inCloseErr
		}
		if outCloseErr != nil {
			return outCloseErr
		}
	}
	return nil
}

func safeFileMode(mode os.FileMode) os.FileMode {
	permissions := mode.Perm() & 0o755
	if permissions == 0 {
		permissions = 0o644
	}
	return permissions
}
