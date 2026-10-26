package extensionsruntime

import (
	"errors"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

const lifecyclePackageSchemaMaximumBytes int64 = 1 << 20

var errLifecyclePackageSchemaNotDeclared = errors.New("exact package Schema entry is not declared")

type exactLifecyclePackageSchema struct {
	File              extensions.ManifestPackageFile
	ManifestReference string
	WireReference     string
	Digest            string
	Body              []byte
}

type lifecyclePackageSchemaLoader struct {
	extension extensions.Extension
	cache     map[string]exactLifecyclePackageSchema
}

func newLifecyclePackageSchemaLoader(extension extensions.Extension) *lifecyclePackageSchemaLoader {
	return &lifecyclePackageSchemaLoader{
		extension: extension,
		cache:     make(map[string]exactLifecyclePackageSchema),
	}
}

func (l *lifecyclePackageSchemaLoader) Load(reference string) (exactLifecyclePackageSchema, error) {
	if l == nil {
		return exactLifecyclePackageSchema{}, errors.New("exact package Schema loader is unavailable")
	}
	reference = strings.TrimSpace(reference)
	file, err := exactLifecyclePackageSchemaFile(l.extension.Manifest.PackageFiles, reference)
	if err != nil {
		return exactLifecyclePackageSchema{}, err
	}
	cacheKey := file.Path + "\x00" + strings.ToLower(file.Digest)
	if cached, found := l.cache[cacheKey]; found {
		cached.ManifestReference = reference
		cached.Body = append([]byte(nil), cached.Body...)
		return cached, nil
	}

	body, actualDigest, err := extensions.ReadExactExtensionDigestFile(
		l.extension, file.Path, file.Digest, lifecyclePackageSchemaMaximumBytes, true,
	)
	if err != nil {
		return exactLifecyclePackageSchema{}, err
	}
	wireReference := strings.TrimSpace(file.ID) + "@" + strings.TrimSpace(file.Version)
	if _, _, err := protocolV2SchemaRef(wireReference); err != nil {
		wireReference = ""
	}
	loaded := exactLifecyclePackageSchema{
		File: file, ManifestReference: reference, WireReference: wireReference,
		Digest: strings.ToLower(actualDigest), Body: append([]byte(nil), body...),
	}
	l.cache[cacheKey] = loaded
	loaded.Body = append([]byte(nil), loaded.Body...)
	return loaded, nil
}

func exactLifecyclePackageSchemaFile(
	files []extensions.ManifestPackageFile,
	reference string,
) (extensions.ManifestPackageFile, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return extensions.ManifestPackageFile{}, errors.New("exact package Schema reference is missing")
	}
	var match *extensions.ManifestPackageFile
	seenPaths := make(map[string]struct{})
	seenIDs := make(map[string]struct{})
	for index := range files {
		file := files[index]
		if file.Kind != "schema" {
			continue
		}
		if _, duplicate := seenPaths[file.Path]; duplicate {
			return extensions.ManifestPackageFile{}, errors.New("duplicate exact package Schema path")
		}
		seenPaths[file.Path] = struct{}{}
		if _, duplicate := seenIDs[file.ID]; duplicate {
			return extensions.ManifestPackageFile{}, errors.New("duplicate exact package Schema id")
		}
		seenIDs[file.ID] = struct{}{}
		if reference != file.Path && reference != file.ID+"@"+file.Version {
			continue
		}
		if match != nil {
			return extensions.ManifestPackageFile{}, errors.New("multiple exact package Schema entries match")
		}
		value := file
		match = &value
	}
	if match == nil {
		return extensions.ManifestPackageFile{}, errLifecyclePackageSchemaNotDeclared
	}
	if strings.TrimSpace(match.Path) == "" || strings.TrimSpace(match.ID) == "" ||
		strings.TrimSpace(match.Digest) == "" {
		return extensions.ManifestPackageFile{}, errors.New("exact package Schema entry is missing")
	}
	return *match, nil
}
