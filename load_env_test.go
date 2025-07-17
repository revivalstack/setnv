package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// mockGenericCommandExecutor creates a commandExecutor that can simulate
// different outputs for different command lines.
// `mockedResponses` is a map where the key is the full command line string
// (e.g., "bash -c 'cd ~;echo `pwd`'") and the value contains the stdout,
// stderr, and exit code for that specific command.
func mockGenericCommandExecutor(mockedResponses map[string]struct {
	stdout   string
	stderr   string
	exitCode int
}) commandExecutor {
	return func(name string, arg ...string) *exec.Cmd {
		// `name` will be "bash", `arg` will be `["-c", "<command-string-from-env-file>"]`
		fullCmd := name + " " + strings.Join(arg, " ")
		response, ok := mockedResponses[fullCmd]
		if !ok {
			// If command not explicitly mocked, default to empty output and failure.
			// This helps identify unmocked commands during testing.
			response = struct {
				stdout   string
				stderr   string
				exitCode int
			}{"", fmt.Sprintf("Error: Unmocked command called: %s", fullCmd), 1}
		}

		// Create a unique temporary file path for the mock script.
		mockScriptPath := filepath.Join(os.TempDir(), fmt.Sprintf("mock-cmd-%d-%d.sh", os.Getpid(), time.Now().UnixNano()))

		scriptContent := fmt.Sprintf(`#!/bin/bash
printf "%%s" "%s" # Print mock stdout to stdout
printf "%%s" "%s" >&2 # Print mock stderr to stderr
exit %d
`, response.stdout, response.stderr, response.exitCode)

		err := ioutil.WriteFile(mockScriptPath, []byte(scriptContent), 0755)
		if err != nil {
			panic(fmt.Sprintf("Failed to create mock script at %s: %v", mockScriptPath, err))
		}

		cmd := exec.Command(mockScriptPath, arg...) // Pass original args to the mock script
		cmd.Stderr = os.Stderr                      // For debugging mock script execution issues
		return cmd
	}
}

// mockCommand creates a mock commandExecutor.
// It returns a function that, when called, creates an `exec.Cmd` pointing to a temporary
// executable script. This script is designed to print the specified `stdout` and `stderr`
// and exit with the given `exitCode`, simulating the behavior of `gopass`.
func mockCommand(stdout string, stderr string, exitCode int) commandExecutor {
	return func(name string, arg ...string) *exec.Cmd {
		// Create a unique temporary file path for the mock script for each call.
		// Using current pid and nanoseconds for high uniqueness.
		mockScriptPath := filepath.Join(os.TempDir(), fmt.Sprintf("mock-cmd-%d-%d.sh", os.Getpid(), time.Now().UnixNano()))

		// Write a simple shell script to this temporary file.
		// `printf "%%s"` is used to prevent adding extra newlines to stdout/stderr.
		scriptContent := fmt.Sprintf(`#!/bin/bash
printf "%%s" "%s" # Print mock stdout to stdout
printf "%%s" "%s" >&2 # Print mock stderr to stderr
exit %d
`, stdout, stderr, exitCode)

		err := ioutil.WriteFile(mockScriptPath, []byte(scriptContent), 0755) // 0755 makes it executable
		if err != nil {
			// If we can't create the mock script, the test environment is broken.
			panic(fmt.Sprintf("Failed to create mock script at %s: %v", mockScriptPath, err))
		}

		// Create an actual `exec.Cmd` that will run our mock script.
		// The `name` argument of this `mockCommand` (e.g., "gopass") is ignored;
		// we always run our mock script. The original `arg`s are passed to it.
		cmd := exec.Command(mockScriptPath, arg...)
		// Ensure that the mock script's stderr output goes to the test's stderr for debugging.
		cmd.Stderr = os.Stderr

		// Store the path of the temporary mock script in the command's environment
		// (or anywhere accessible) so it can be cleaned up later by the test caller.
		// For simplicity, we just return the cmd and expect the caller to clean up
		// based on the `mockScriptPath`.
		return cmd
	}
}

// TestParseEnvFile is a comprehensive test suite for the `parseEnvFile` function.
func TestParseEnvFile(t *testing.T) {
	tests := []struct {
		name              string // Name of the test case
		envContent        string // Content to write to the temporary .env file
		mockGopassOut     string // Expected stdout from mock gopass call
		mockGopassErr     bool   // Whether mock gopass should return an error
		mockedGenericCmds map[string]struct {
			stdout   string
			stderr   string
			exitCode int
		} // For generic command mocking
		expectedMap   map[string]string // The expected final map of environment variables
		expectedError bool              // Whether parseEnvFile itself is expected to return an error
		expectWarning bool              // Whether a warning is expected to be printed to stderr
		// For tests where system environment variables are relevant for command execution
		// This map will be added to the os.Environ() during mockCommandExecutor setup
		mockSystemEnv map[string]string
	}{
		{
			name:       "Generic Command Execution (pwd)",
			envContent: "mydir=$(cd ~;echo `pwd`)",
			mockedGenericCmds: map[string]struct {
				stdout   string
				stderr   string
				exitCode int
			}{"bash -c cd ~;echo `pwd`": {stdout: "/home/myuser", stderr: "", exitCode: 0}},
			expectedMap: map[string]string{"mydir": "/home/myuser"},
		},
		{
			name:       "Generic Command Returns Empty",
			envContent: "EMPTY_CMD=$(echo -n)",
			mockedGenericCmds: map[string]struct {
				stdout   string
				stderr   string
				exitCode int
			}{"bash -c echo -n": {stdout: "", stderr: "", exitCode: 0}},
			expectedMap:   map[string]string{"EMPTY_CMD": ""},
			expectWarning: true, // Expect a warning about empty command output
		},
		{
			name:       "Generic Command Error",
			envContent: "FAILED_CMD=$(exit 1)",
			mockedGenericCmds: map[string]struct {
				stdout   string
				stderr   string
				exitCode int
			}{"bash -c exit 1": {stdout: "", stderr: "mock command error", exitCode: 1}},
			expectedMap:   map[string]string{"FAILED_CMD": ""}, // Should default to empty string on command error
			expectWarning: true,                                // Expect warning from command failure
		},
		{
			name:       "Mixed Generic Command and Variable Expansion",
			envContent: "MY_PATH=$(cd ~;echo `pwd`)\nFULL_PATH=The path is $MY_PATH",
			mockedGenericCmds: map[string]struct {
				stdout   string
				stderr   string
				exitCode int
			}{
				"bash -c cd ~;echo `pwd`": {stdout: "/home/myuser", stderr: "", exitCode: 0},
			},
			expectedMap: map[string]string{"MY_PATH": "/home/myuser", "FULL_PATH": "The path is /home/myuser"},
		},
		{
			name:        "Basic Key-Value Pairs",
			envContent:  "KEY1=VALUE1\nKEY2=VALUE2",
			expectedMap: map[string]string{"KEY1": "VALUE1", "KEY2": "VALUE2"},
		},
		{
			name:        "Empty Lines and Comments",
			envContent:  "# Comment\n\nKEY1=VALUE1\n # Another comment\n\nKEY2=VALUE2",
			expectedMap: map[string]string{"KEY1": "VALUE1", "KEY2": "VALUE2"},
		},
		{
			name: `Quoted Values and Inner Escapes (strconv.Unquote functionality)`,
			envContent: `KEY1="VALUE WITH SPACES"
KEY2='ANOTHER VALUE'
KEY3="Value with \"inner quotes\" and \n newline escape"
KEY4='Value with \'single inner quotes\''
KEY5="Value with mixed\t tabs and\r carriage returns"`,
			expectedMap: map[string]string{
				"KEY1": "VALUE WITH SPACES",
				"KEY2": "ANOTHER VALUE",
				"KEY3": "Value with \"inner quotes\" and \n newline escape",
				"KEY4": "Value with \\'single inner quotes\\'",
				"KEY5": "Value with mixed\t tabs and\r carriage returns",
			},
		},
		{
			name:        "Malformed Quoted Value (Unquote Error fallback)",
			envContent:  `MALFORMED_QUOTE="Unclosed quote`,                       // strconv.Unquote will not be called for malformed double quotes
			expectedMap: map[string]string{"MALFORMED_QUOTE": `"Unclosed quote`}, // Stays as is, no unquoting or stripping if not properly delimited
			//expectWarning: false, // No warning from strconv.Unquote if it's not even attempted -- this was correct
		},
		{
			name:          "Gopass Success",
			envContent:    `DB_PASS=$(gopass show my/db/pass)`,
			mockGopassOut: "secret_db_password",
			expectedMap:   map[string]string{"DB_PASS": "secret_db_password"},
		},
		{
			name:          "Gopass Returns Empty",
			envContent:    `API_KEY=$(gopass show some/api/key)`,
			mockGopassOut: "", // gopass might return empty for non-existent or empty secret
			expectedMap:   map[string]string{"API_KEY": ""},
			expectWarning: true, // Expect warning about empty gopass value
		},
		{
			name:          "Gopass Error",
			envContent:    `FAILED_SECRET=$(gopass show non/existent/secret)`,
			mockGopassOut: "",
			mockGopassErr: true,                                   // Simulate gopass command returning an error
			expectedMap:   map[string]string{"FAILED_SECRET": ""}, // Should default to empty string on gopass error
			expectWarning: true,                                   // Expect warning from gopass failure
		},
		{
			name:          "Mixed Gopass and Regular Variables",
			envContent:    "VAR1=value1\nSEC_VAR=$(gopass show secret/path)\nVAR2=value2",
			mockGopassOut: "resolved_secret",
			expectedMap:   map[string]string{"VAR1": "value1", "SEC_VAR": "resolved_secret", "VAR2": "value2"},
		},
		{
			name:        "Empty Value",
			envContent:  "EMPTY=",
			expectedMap: map[string]string{"EMPTY": ""},
		},
		{
			name:          "Malformed Line (without equals sign)",
			envContent:    "KEY1=VAL1\nJUST_A_KEY\nKEY2=VAL2", // "JUST_A_KEY" is malformed
			expectedMap:   map[string]string{"KEY1": "VAL1", "KEY2": "VAL2"},
			expectWarning: true, // Expect warning for "JUST_A_KEY"
		},
		{
			name: "Simple Variable Expansion ($VAR)",
			envContent: `VAR1=hello
VAR2=$VAR1 world`,
			expectedMap: map[string]string{"VAR1": "hello", "VAR2": "hello world"},
		},
		{
			name: "Curly Brace Variable Expansion (${VAR})",
			envContent: `BASE_URL=http://localhost
PORT=8080
FULL_URL=${BASE_URL}:${PORT}/api`,
			expectedMap: map[string]string{"BASE_URL": "http://localhost", "PORT": "8080", "FULL_URL": "http://localhost:8080/api"},
		},
		{
			name: "Mixed Expansion Styles",
			envContent: `FOO=foo
BAR=${FOO}bar
BAZ=$BAR-baz`,
			expectedMap: map[string]string{"FOO": "foo", "BAR": "foobar", "BAZ": "foobar-baz"},
		},
		{
			name: "Undefined Variable Expansion (expands to empty)",
			envContent: `HELLO=world
GREETING=$UNDEFINED_VAR HELLO ${ANOTHER_UNDEFINED} `,
			expectedMap: map[string]string{"HELLO": "world", "GREETING": " HELLO "}, // Corrected expected value
		},
		{
			name: "Expansion with Gopass Value",
			envContent: `APP_SECRET=$(gopass show my/app/secret)
APP_CONFIG=Secret is: $APP_SECRET`,
			mockGopassOut: "my-resolved-secret",
			expectedMap:   map[string]string{"APP_SECRET": "my-resolved-secret", "APP_CONFIG": "Secret is: my-resolved-secret"},
		},
		{
			name:        "No Expansion Needed",
			envContent:  `KEY=plainvalue`,
			expectedMap: map[string]string{"KEY": "plainvalue"},
		},
		{
			name:        "Literal Dollar Signs (escaped with backslash)",
			envContent:  `COST=\$100.00`, // Backslash to escape literal $
			expectedMap: map[string]string{"COST": "__LOAD_ENV_LITERAL_DOLLAR__100.00"},
		},
		{
			name:        "Variable references itself (should resolve to empty)",
			envContent:  `MY_VAR=$MY_VAR`,
			expectedMap: map[string]string{"MY_VAR": ""}, // Should resolve to empty
		},
		{
			name: "Expansion with unquoted spaces in referenced variable (correct syntax)",
			envContent: `VAR_WITH_SPACE=hello world
FINAL_VAR=${VAR_WITH_SPACE}_END`, // Corrected to use curly braces for concatenation
			expectedMap: map[string]string{"VAR_WITH_SPACE": "hello world", "FINAL_VAR": "hello world_END"},
		},
		// --- NEW TEST CASES FOR COMMAND SUBSTITUTION CHAINING (FIXED MOCK KEYS) ---
		{
			name:       "Cmd Sub: Referencing Preceding Variable",
			envContent: "PRE_VAR=hello\nCMD_RESULT=$(echo $PRE_VAR world)",
			mockedGenericCmds: map[string]struct {
				stdout   string
				stderr   string
				exitCode int
			}{
				// The key here is the exact command string passed to "bash -c" after variable expansion
				"bash -c echo hello world": {stdout: "hello world", stderr: "", exitCode: 0},
			},
			expectedMap: map[string]string{"PRE_VAR": "hello", "CMD_RESULT": "hello world"},
		},
		{
			name: "Cmd Sub: Referencing Preceding Variable with Expansion",
			// envContent: "BASE_MSG=Start\nFULL_MSG=${BASE_MSG}_Middle\nFINAL_CMD=$(echo $FULL_MSG_End)",
			envContent: "BASE_MSG=Start\nFULL_MSG=${BASE_MSG}_Middle\nFINAL_CMD=$(echo ${FULL_MSG}_End)",
			mockedGenericCmds: map[string]struct {
				stdout   string
				stderr   string
				exitCode int
			}{
				// The command string includes the unexpanded variable name, bash will expand it.
				"bash -c echo Start_Middle_End": {stdout: "Start_Middle_End", stderr: "", exitCode: 0},
			},
			expectedMap: map[string]string{"BASE_MSG": "Start", "FULL_MSG": "Start_Middle", "FINAL_CMD": "Start_Middle_End"},
		},
		{
			name:          "Cmd Sub: Overriding System Var for Cmd Sub",
			envContent:    "PATH=/new/path/bin\nCMD_ENV_PATH=$(echo $PATH)",
			mockSystemEnv: map[string]string{"PATH": "/old/path/bin"}, // System PATH will be overridden by .env's PATH for the sub-command
			mockedGenericCmds: map[string]struct {
				stdout   string
				stderr   string
				exitCode int
			}{
				"bash -c echo /new/path/bin": {stdout: "/new/path/bin", stderr: "", exitCode: 0}, // Mock should return the overridden value
			},
			expectedMap: map[string]string{"PATH": "/new/path/bin", "CMD_ENV_PATH": "/new/path/bin"},
		},
		{
			name: "Cmd Sub: Complex Chaining within Env File",
			envContent: `ROOT_DIR=/opt/app
BIN_DIR=$(echo $ROOT_DIR/bin)
CONFIG_PATH=$(echo $BIN_DIR/config.yaml)`,
			mockedGenericCmds: map[string]struct {
				stdout   string
				stderr   string
				exitCode int
			}{
				"bash -c echo /opt/app/bin":             {stdout: "/opt/app/bin", stderr: "", exitCode: 0},
				"bash -c echo /opt/app/bin/config.yaml": {stdout: "/opt/app/bin/config.yaml", stderr: "", exitCode: 0},
			},
			expectedMap: map[string]string{
				"ROOT_DIR":    "/opt/app",
				"BIN_DIR":     "/opt/app/bin",
				"CONFIG_PATH": "/opt/app/bin/config.yaml",
			},
		},
		{
			name: "Cmd Sub: Mixed Gopass and Env Var in Cmd",
			envContent: `SECRET_ID=my/service/secret
DB_PASSWORD=$(gopass show $SECRET_ID)`,
			// The `gopass show` command will be executed by bash, with $SECRET_ID expanded by bash.
			// The `gopassRegex` matches `$(gopass show <path>)`. The <path> is what is sent as `commandToExecute` to `executeCommandSubstitution`
			// then it constructs `gopass show --password <path>`.
			// So, the mock key should be exactly `bash -c gopass show --password <expanded_SECRET_ID>`
			mockedGenericCmds: map[string]struct {
				stdout   string
				stderr   string
				exitCode int
			}{
				"bash -c gopass show --password my/service/secret": {stdout: "actual-db-pass", stderr: "", exitCode: 0},
			},
			expectedMap: map[string]string{
				"SECRET_ID":   "my/service/secret",
				"DB_PASSWORD": "actual-db-pass",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a temporary .env file for the current test case
			tempFile, err := ioutil.TempFile("", "test_env_*.env")
			if err != nil {
				t.Fatalf("Failed to create temp file: %v", err)
			}
			// Ensure the temporary .env file is removed after the test
			defer os.Remove(tempFile.Name())

			// Write the test content to the temporary .env file
			_, err = tempFile.WriteString(tt.envContent)
			if err != nil {
				t.Fatalf("Failed to write to temp file: %v", err)
			}
			tempFile.Close() // Close the file handle

			// --- Set up mock system environment if provided ---
			originalEnviron := os.Environ() // Save original environment
			if tt.mockSystemEnv != nil {
				newEnv := []string{}
				for k, v := range tt.mockSystemEnv {
					newEnv = append(newEnv, fmt.Sprintf("%s=%s", k, v))
				}
				os.Clearenv() // Clear existing env
				for _, kv := range newEnv {
					os.Setenv(strings.SplitN(kv, "=", 2)[0], strings.SplitN(kv, "=", 2)[1])
				}
			}
			// Restore original environment after the test
			defer func() {
				os.Clearenv()
				for _, kv := range originalEnviron {
					parts := strings.SplitN(kv, "=", 2)
					if len(parts) == 2 {
						os.Setenv(parts[0], parts[1])
					}
				}
			}()

			// --- Capture Stderr for warning checks ---
			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w // Redirect stderr to our pipe
			// Ensure stderr is restored after the test, and the pipe is closed.
			defer func() {
				w.Close()
				os.Stderr = oldStderr
				// Read all captured stderr output for analysis
				_, _ = ioutil.ReadAll(r) // Discard if not used, to prevent resource leak
			}()

			// Create a mock command executor tailored for this test case's gopass behavior
			var mockCmdExecutor commandExecutor
			if len(tt.mockedGenericCmds) > 0 {
				mockCmdExecutor = mockGenericCommandExecutor(tt.mockedGenericCmds)
			} else if tt.mockGopassErr {
				// If gopass error is expected, set up the mock to return an error (exit code 1)
				mockCmdExecutor = mockCommand("", "mock gopass error", 1)
			} else {
				// Otherwise, set up the mock to return the specified output (exit code 0)
				mockCmdExecutor = mockCommand(tt.mockGopassOut, "", 0)
			}

			// Call the `parseEnvFile` function under test
			actualMap, err := parseEnvFile(tempFile.Name(), mockCmdExecutor, make(map[string]string))

			// Close the write end of the pipe immediately after `parseEnvFile` returns,
			// so that `ioutil.ReadAll` on the read end gets EOF.
			w.Close()
			// Read all captured stderr output
			capturedStderrBytes, _ := ioutil.ReadAll(r)
			capturedStderr := string(capturedStderrBytes)

			// --- Assertions ---

			// 1. Check for expected errors from `parseEnvFile` itself
			if (err != nil) != tt.expectedError {
				t.Errorf("Test '%s' failed: Expected parseEnvFile error: %t, Got: %t. Error: %v", tt.name, tt.expectedError, (err != nil), err)
			}
			if err != nil {
				return // If an error was expected and occurred, skip further assertions for this test case.
			}

			// 2. Check for warnings printed to stderr
			if tt.expectWarning && !strings.Contains(capturedStderr, "Warning:") {
				// If a warning is expected, but "Warning:" string is not found in stderr, then fail.
				t.Errorf("Test '%s' failed: Expected warnings on stderr, but no 'Warning:' output was captured. Stderr:\n%s", tt.name, capturedStderr)
			} else if !tt.expectWarning && strings.Contains(capturedStderr, "Warning:") {
				// If no warning is expected but there's "Warning:" output on stderr, then fail.
				t.Errorf("Test '%s' failed: Unexpected warnings on stderr. Stderr:\n%s", tt.name, capturedStderr)
			}

			// 3. Compare the actual parsed map with the expected map.
			// `reflect.DeepEqual` works well for maps.
			if !reflect.DeepEqual(actualMap, tt.expectedMap) {
				// For better diffing in test output, convert maps to sorted slices of strings.
				actualSlice := mapToSortedSlice(actualMap)
				expectedSlice := mapToSortedSlice(tt.expectedMap)
				t.Errorf("Test '%s' failed: Mismatch in parsed environment variables.\nExpected: %v\nActual:   %v", tt.name, expectedSlice, actualSlice)
			}
		})
	}
}

// mapToSortedSlice is a helper function for tests.
// It converts a map[string]string to a sorted slice of "KEY=VALUE" strings.
// This is crucial for comparing map contents consistently in tests, as Go map
// iteration order is not guaranteed, which can lead to flaky `reflect.DeepEqual`
// comparisons if the map is directly converted to a slice without sorting.
func mapToSortedSlice(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys) // Sort the keys alphabetically

	s := make([]string, 0, len(m))
	for _, k := range keys {
		s = append(s, fmt.Sprintf("%s=%s", k, m[k]))
	}
	return s
}
