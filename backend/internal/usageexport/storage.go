package usageexport

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type LocalStore struct{ Root string }

func (s LocalStore) path(key string) (string, error) {
	if !filepath.IsLocal(key) || strings.Contains(key, "\\") {
		return "", fmt.Errorf("invalid export key")
	}
	return filepath.Join(s.Root, filepath.FromSlash(key)), nil
}
func (s LocalStore) Put(ctx context.Context, key, source string) error {
	target, err := s.path(key)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".part-")
	if err != nil {
		return err
	}
	name := out.Name()
	defer func() { _ = os.Remove(name) }()
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, &contextReader{ctx, in})
	if err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(name, target)
}
func (s LocalStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := s.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}
func (s LocalStore) Delete(_ context.Context, key string) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	entries, readErr := os.ReadDir(filepath.Dir(p))
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), filepath.Base(p)+".part-") {
			if er := os.Remove(filepath.Join(filepath.Dir(p), entry.Name())); er != nil && !os.IsNotExist(er) {
				return er
			}
		}
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
