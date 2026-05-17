package config

import (
	"errors"
	"os"
)

// ErrNotFound is returned by a Source when the underlying config file
// does not exist. NewUnion silently skips sources that return this error.
var ErrNotFound = errors.New("config: not found")

// Source provides configuration. Use FromFile to construct file-backed
// sources and NewUnion to layer multiple sources.
type Source interface {
	Get() (Config, error)
}

// rawProvider is an optional interface implemented by sources that can
// expose raw TOML data for precise merging inside a union. It is an
// internal optimisation — callers never see it.
//
// Without it, union falls back to Config-level merging, which is correct
// for all cases except one edge: a lower-priority source that explicitly
// sets supervisor will be overridden by a higher-priority source that
// doesn't set it (because the default-apply step has already filled the
// field in the higher-priority source's Get() result). Sources created
// by this package always implement rawProvider, so the edge case only
// surfaces with third-party Source implementations.
type rawProvider interface {
	getRaw() (rawConfig, error)
}

// FromFile returns a Source backed by the file at path. Get() returns
// ErrNotFound when the file does not exist.
func FromFile(path string) Source {
	return fileSource{path: path}
}

// NewUnion returns a Source that merges sources in left-to-right priority
// order (right-most source wins). Sources that return ErrNotFound are
// silently skipped. Any other error aborts Get().
func NewUnion(sources ...Source) Source {
	return unionSource{sources: sources}
}

// ---------------------------------------------------------------------------

type fileSource struct{ path string }

func (f fileSource) Get() (Config, error) {
	raw, err := f.getRaw()
	if err != nil {
		return Config{}, err
	}
	return raw.toConfig()
}

func (f fileSource) getRaw() (rawConfig, error) {
	raw, err := loadRaw(f.path)
	if err != nil {
		if os.IsNotExist(err) || isNotFoundErr(err) {
			return rawConfig{}, ErrNotFound
		}
		return rawConfig{}, err
	}
	return raw, nil
}

// ---------------------------------------------------------------------------

type unionSource struct{ sources []Source }

func (u unionSource) Get() (Config, error) {
	// Collect raw configs from sources that implement rawProvider
	// (our own fileSource does). Third-party sources fall back to
	// Config-level merging.
	var raws []rawConfig
	var cfgs []Config

	for _, s := range u.sources {
		if rp, ok := s.(rawProvider); ok {
			raw, err := rp.getRaw()
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return Config{}, err
			}
			raws = append(raws, raw)
		} else {
			cfg, err := s.Get()
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return Config{}, err
			}
			cfgs = append(cfgs, cfg)
		}
	}

	if len(raws) == 0 && len(cfgs) == 0 {
		return Config{}, ErrNotFound
	}

	// Merge raw layers first, then convert, then apply Config-level layers.
	var result Config
	if len(raws) > 0 {
		merged := raws[0]
		for _, r := range raws[1:] {
			merged = mergeRaw(merged, r)
		}
		var err error
		result, err = merged.toConfig()
		if err != nil {
			return Config{}, err
		}
	}
	for _, cfg := range cfgs {
		result = Merge(result, cfg)
	}
	return result, nil
}

// isNotFoundErr reports whether err wraps an "open ... no such file" error
// from the TOML decoder (which does not return a bare os.ErrNotExist).
func isNotFoundErr(err error) bool {
	var pe *os.PathError
	return errors.As(err, &pe) && os.IsNotExist(pe)
}
