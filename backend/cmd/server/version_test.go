//go:build unit

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// -version prints exactly one plain line, starting with the product version,
// so deploy scripts can take the first version on the first line.
func TestPrintVersionWritesOnePlainLine(t *testing.T) {
	var out bytes.Buffer
	printVersion(&out)

	line := out.String()
	require.Equal(t, 1, strings.Count(line, "\n"))
	require.True(t, strings.HasSuffix(line, "\n"))
	require.True(t, strings.HasPrefix(line, "XY2API "+Version+" "), line)
	require.Contains(t, line, "commit: "+Commit)
}
