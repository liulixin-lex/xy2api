//go:build unit

package handler

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
)

// The characterization data is large and generated, so it is stored compressed.
const replayGoldenPath = "testdata/collaboration_replay_golden.json.gz"

func readReplayGolden() ([]byte, error) {
	f, err := os.Open(replayGoldenPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	z, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer func() { _ = z.Close() }()
	return io.ReadAll(z)
}

func writeReplayGolden(raw []byte) error {
	var buf bytes.Buffer
	z, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := z.Write(raw); err != nil {
		return err
	}
	if err := z.Close(); err != nil {
		return err
	}
	return os.WriteFile(replayGoldenPath, buf.Bytes(), 0o600)
}
