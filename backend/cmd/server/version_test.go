//go:build unit

package main

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
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

func TestVersionCLIStdout(t *testing.T) {
	if os.Getenv("XY2API_TEST_VERSION_CLI") == "1" {
		os.Args = []string{"xy2api", "-version"}
		flag.CommandLine = flag.NewFlagSet("xy2api", flag.ExitOnError)
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestVersionCLIStdout$")
	cmd.Env = append(os.Environ(), "XY2API_TEST_VERSION_CLI=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), stderr.String())
	require.Equal(t, "XY2API "+Version+" (Sub2API compatibility: "+Sub2APICompatVersion+", commit: "+Commit+", built: "+Date+")\nPASS\n", stdout.String())
	require.Empty(t, stderr.String())
}
