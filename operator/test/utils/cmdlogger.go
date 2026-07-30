/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package utils

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:staticcheck,revive
)

// CmdLogger captures every kubectl/exec command and its output to a log file
// instead of printing to the Ginkgo console. The log file is written to
// ARTIFACT_DIR (for CI artifact upload) or os.TempDir() (local runs).
//
// It is safe for concurrent use from multiple goroutines.
type CmdLogger struct {
	mu   sync.Mutex
	file *os.File
}

// NewCmdLogger creates a log file named "<suiteName>-kubectl.log" in
// ARTIFACT_DIR (or os.TempDir()) and returns a logger that writes to it.
func NewCmdLogger(suiteName string) (*CmdLogger, error) {
	logDir := os.Getenv("ARTIFACT_DIR")
	if logDir == "" {
		logDir = os.TempDir()
	}
	f, err := os.Create(filepath.Join(logDir, suiteName+"-kubectl.log"))
	if err != nil {
		return nil, fmt.Errorf("creating cmd log: %w", err)
	}
	_, _ = fmt.Fprintf(GinkgoWriter, "kubectl log: %s\n", f.Name())
	return &CmdLogger{file: f}, nil
}

// Close flushes and closes the underlying log file.
func (l *CmdLogger) Close() {
	if l == nil || l.file == nil {
		return
	}
	_ = l.file.Close()
}

// Run executes cmd, logs the command and output to the file, and returns the
// combined output. On failure the error includes the command, exit status, and
// output for inline diagnostics.
func (l *CmdLogger) Run(cmd *exec.Cmd) (string, error) {
	command := strings.Join(cmd.Args, " ")
	start := time.Now()
	output, err := cmd.CombinedOutput()

	if l != nil && l.file != nil {
		l.mu.Lock()
		_, _ = fmt.Fprintf(l.file, "$ %s  [%s]\n%s", command, time.Since(start).Round(time.Millisecond), string(output))
		if err != nil {
			_, _ = fmt.Fprintf(l.file, "EXIT: %v\n", err)
		}
		_, _ = fmt.Fprintln(l.file)
		l.mu.Unlock()
	}

	if err != nil {
		return string(output), fmt.Errorf("%s failed with error: (%v) %s", command, err, string(output))
	}
	return string(output), nil
}

// StringReader creates a *strings.Reader — a convenience wrapper to avoid
// importing "strings" in every test suite just for this.
func StringReader(s string) *strings.Reader {
	return strings.NewReader(s)
}
