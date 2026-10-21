package materializer

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
)

const maxDockerManifestBytes = 1 << 20

var (
	digestFilePattern = regexp.MustCompile(`^([0-9a-f]{64})\.json$`)
	ociBlobPattern    = regexp.MustCompile(`^blobs/sha256/([0-9a-f]{64})$`)
)

type dockerArchiveManifest struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
	Layers   []string `json:"Layers"`
}

type ociIndex struct {
	SchemaVersion int `json:"schemaVersion"`
	Manifests     []struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"manifests"`
}

type ociManifest struct {
	SchemaVersion int `json:"schemaVersion"`
	Config        struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		Digest string `json:"digest"`
	} `json:"layers"`
}

func inspectDockerArchive(archivePath string) (string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open Docker archive: %w", err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
	var document, indexDocument []byte
	entries := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read Docker archive: %w", err)
		}
		entries++
		if entries > 100_000 || !safeArchivePath(header.Name) ||
			(header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeDir) {
			return "", errors.New("Docker archive contains an unsafe or excessive entry")
		}
		if header.Name == "manifest.json" {
			if document != nil || header.Size < 1 || header.Size > maxDockerManifestBytes {
				return "", errors.New("Docker archive must contain one bounded manifest.json")
			}
			document, err = io.ReadAll(io.LimitReader(reader, maxDockerManifestBytes+1))
			if err != nil || len(document) > maxDockerManifestBytes {
				return "", errors.New("Docker archive manifest is invalid")
			}
		}
		if header.Name == "index.json" {
			if indexDocument != nil || header.Size < 1 || header.Size > maxDockerManifestBytes {
				return "", errors.New("Docker archive must contain one bounded index.json")
			}
			indexDocument, err = io.ReadAll(io.LimitReader(reader, maxDockerManifestBytes+1))
			if err != nil || len(indexDocument) > maxDockerManifestBytes {
				return "", errors.New("Docker archive index is invalid")
			}
		}
	}
	var manifests []dockerArchiveManifest
	if json.Unmarshal(document, &manifests) != nil || len(manifests) != 1 || len(manifests[0].Layers) == 0 {
		return "", errors.New("Docker archive must contain exactly one image")
	}
	if len(manifests[0].RepoTags) != 0 {
		return "", errors.New("Docker archive must not import repository tags")
	}
	configDigest, ok := dockerArchiveDigest(manifests[0].Config, true)
	if !ok {
		return "", errors.New("Docker archive has an invalid image configuration path")
	}
	expected := map[string]string{manifests[0].Config: configDigest}
	imageID := "sha256:" + configDigest
	ociConfig := ociBlobPattern.MatchString(manifests[0].Config)
	ociManifestPath := ""
	var ociManifestSize int64
	if ociConfig {
		var index ociIndex
		if json.Unmarshal(indexDocument, &index) != nil || index.SchemaVersion != 2 || len(index.Manifests) != 1 ||
			!strings.HasPrefix(index.Manifests[0].Digest, "sha256:") || index.Manifests[0].Size < 1 || index.Manifests[0].Size > maxDockerManifestBytes {
			return "", errors.New("Docker archive has an invalid OCI index")
		}
		manifestDigest := strings.TrimPrefix(index.Manifests[0].Digest, "sha256:")
		ociManifestPath = "blobs/sha256/" + manifestDigest
		if !ociBlobPattern.MatchString(ociManifestPath) {
			return "", errors.New("Docker archive has an invalid OCI manifest digest")
		}
		expected[ociManifestPath] = manifestDigest
		imageID = index.Manifests[0].Digest
		ociManifestSize = index.Manifests[0].Size
	}
	for _, layer := range manifests[0].Layers {
		layerDigest, ociLayer := dockerArchiveDigest(layer, false)
		if !safeArchivePath(layer) || (!ociLayer && !strings.HasSuffix(layer, "/layer.tar")) {
			return "", errors.New("Docker archive contains an invalid layer path")
		}
		if _, duplicate := expected[layer]; duplicate {
			return "", errors.New("Docker archive contains duplicate image entries")
		}
		expected[layer] = layerDigest
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	reader = tar.NewReader(file)
	seen := make(map[string]bool, len(expected))
	var ociManifestDocument []byte
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		digestName, wanted := expected[header.Name]
		if !wanted {
			continue
		}
		if seen[header.Name] {
			return "", errors.New("Docker archive contains duplicate image entries")
		}
		seen[header.Name] = true
		if digestName == "" {
			continue
		}
		digest := sha256.New()
		source := io.Reader(reader)
		if header.Name == ociManifestPath {
			if header.Size != ociManifestSize {
				return "", errors.New("Docker archive OCI manifest size does not match its index")
			}
			ociManifestDocument = make([]byte, header.Size)
			if _, err := io.ReadFull(reader, ociManifestDocument); err != nil {
				return "", fmt.Errorf("read Docker OCI manifest: %w", err)
			}
			source = strings.NewReader(string(ociManifestDocument))
		}
		if _, err := io.Copy(digest, source); err != nil {
			return "", fmt.Errorf("hash Docker image configuration: %w", err)
		}
		got := fmt.Sprintf("%x", digest.Sum(nil))
		if got != digestName {
			return "", errors.New("Docker image entry digest does not match its filename")
		}
	}
	for entry := range expected {
		if !seen[entry] {
			return "", errors.New("Docker image entry is missing")
		}
	}
	if ociConfig {
		var descriptor ociManifest
		if json.Unmarshal(ociManifestDocument, &descriptor) != nil || descriptor.SchemaVersion != 2 ||
			descriptor.Config.Digest != "sha256:"+configDigest || len(descriptor.Layers) != len(manifests[0].Layers) {
			return "", errors.New("Docker archive OCI manifest does not match manifest.json")
		}
		for index, layer := range descriptor.Layers {
			if layer.Digest != "sha256:"+strings.TrimPrefix(manifests[0].Layers[index], "blobs/sha256/") {
				return "", errors.New("Docker archive OCI layers do not match manifest.json")
			}
		}
	}
	return imageID, nil
}

func dockerArchiveDigest(name string, configuration bool) (string, bool) {
	if match := ociBlobPattern.FindStringSubmatch(name); match != nil {
		return match[1], true
	}
	if configuration {
		if match := digestFilePattern.FindStringSubmatch(name); match != nil {
			return match[1], true
		}
	}
	return "", false
}

func safeArchivePath(name string) bool {
	cleanName := strings.TrimSuffix(name, "/")
	return cleanName != "" && !path.IsAbs(cleanName) && path.Clean(cleanName) == cleanName && cleanName != "." && cleanName != ".." && !strings.HasPrefix(cleanName, "../")
}
