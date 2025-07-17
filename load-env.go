package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const (
	version = "0.5.0"

	// DefaultConfigDir defines the default centralized directory for .env files.
	// It's relative to the user's home directory (e.g., ~/.config/load-env).
	// This can be overridden by the LOAD_ENV_CONFIG_DIR environment variable.
	DefaultConfigDir = ".config/load-env"

	// defaultShell is the shell to launch when `load-env <id>` is called
	// without a specified executable.
	defaultShell = "bash"

	// A unique string unlikely to appear in values
	literalDollarPlaceholder = "__LOAD_ENV_LITERAL_DOLLAR__"
)

// commandExecutor is a type that represents a function capable of executing a command.
// This abstraction is crucial for testing, allowing us to easily mock `os/exec.Command`
// without actually running external processes during tests.
type commandExecutor func(name string, arg ...string) *exec.Cmd

// defaultCommandExecutor is the actual `os/exec.Command` function used in production.
var defaultCommandExecutor commandExecutor = exec.Command

// gopassRegex identifies `$(gopass show <path>)` patterns for specific handling.
// It captures the path as the first group.
var gopassRegex = regexp.MustCompile(`\$\(gopass(?: show)?(?:(?:\s+[-]{1,2}[a-zA-Z0-9_]+(?:[^)\s]+)?)*)\s*([^)]+)\)`)

// alternateCommandRegex identifies any `$[command args...]` pattern.
// It captures the entire command string inside the brackets as the first group.
var alternateCommandRegex = regexp.MustCompile(`\$\[([^\]]+)\]`)

// genericCommandRegex identifies any `$(command args...)` pattern.
// It captures the entire command string inside the parentheses as the first group.
var genericCommandRegex = regexp.MustCompile(`\$\(([^)]+)\)`)

// variableExpansionRegex is a regular expression to find occurrences of
// `$VAR` or `${VAR}` patterns within a string.
// Group 1 captures the variable name for `$VAR` (e.g., `VAR_NAME`).
// Group 2 captures the variable name for `${VAR}` (e.g., `VAR_NAME`).
var variableExpansionRegex = regexp.MustCompile(`\$(?:([a-zA-Z_][a-zA-Z0-9_]*)|{([a-zA-Z_][a-zA-Z0-9_]*)})`)

// applyCommandSubstitution replaces command substitution patterns (e.g., $(...) or $[...])
// in the given value string using the provided regex.
func applyCommandSubstitution(
	value string,
	r *regexp.Regexp, // The regex to use (genericCommandRegex or alternateCommandRegex)
	key string,
	envFilePath string,
	lineNum int,
	cmdExecutor commandExecutor,
	inheritedEnvMap map[string]string,
	initialEnvMap map[string]string,
	combinedEnvForLookup map[string]string,
) string {
	return r.ReplaceAllStringFunc(value, func(matchStr string) string {
		matches := r.FindStringSubmatch(matchStr)
		if len(matches) < 2 || matches[1] == "" { // Should not happen if regex matched correctly and captured
			fmt.Fprintf(os.Stderr, " » load-env: Warning: Command substitution regex matched but failed to extract command for variable '%s' on line %d in '%s'. Match: '%s'.\n", key, lineNum, envFilePath, matchStr)
			return matchStr // Return original match if command extraction fails
		}
		commandToExecute := matches[1]

		output, err := executeCommandSubstitution(key, commandToExecute, envFilePath, lineNum, cmdExecutor, inheritedEnvMap, initialEnvMap)
		if err != nil {
			fmt.Fprintf(os.Stderr, " » load-env: Warning: %v. Value set to empty.\n", err)
			return ""
		}

		// Crucially: Expand variables *within the command's output*
		// This is a recursive call to expandVarsInString
		output = expandVarsInString(output, combinedEnvForLookup)

		if output == "" {
			fmt.Fprintf(os.Stderr, " » load-env: Warning: command '%s' for variable '%s' returned an empty value on line %d in '%s'.\n", commandToExecute, key, lineNum, envFilePath)
		}
		return output
	})
}

// executeCommandSubstitution runs a command string using the default shell
// and returns its standard output.
// It also directs the command's standard error to load-env's standard error.
func executeCommandSubstitution(key, commandString, envFilePath string, lineNum int, cmdExecutor commandExecutor, inheritedEnvMap map[string]string, currentEnvMap map[string]string) (string, error) {
	cmd := cmdExecutor(defaultShell, "-c", commandString)
	cmd.Stderr = os.Stderr // Direct command's stderr to `load-env`'s stderr for visibility.

	// Build the environment for the sub-command.
	// `subCmdEnvMap` is the environment that the executed command (e.g., `bash -c ...`) will inherit.
	// It is constructed by merging `inheritedEnvMap` and `currentEnvMap`.
	//
	// `inheritedEnvMap` represents the accumulated environment from the base shell (os.Environ())
	// and all previously processed .env files in the chaining sequence.
	//
	// `currentEnvMap` contains variables parsed from the *current* .env file
	// up to the line where this command substitution occurs. This includes variables
	// that may override existing ones from `inheritedEnvMap`, or add new ones defined locally.
	//
	// Variables from `currentEnvMap` take precedence over those in `inheritedEnvMap` if keys conflict.
	subCmdEnvMap := mergeMaps(inheritedEnvMap, currentEnvMap)

	// Convert the map to a slice of "KEY=VALUE" strings for cmd.Env
	subCmdEnvSlice := mapToSlice(subCmdEnvMap)

	cmd.Env = subCmdEnvSlice

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Include stderr output from the failed command in the error message
			return "", fmt.Errorf(" » command '%s' for variable '%s' on line %d in '%s' failed with exit code %d: %s", commandString, key, lineNum, envFilePath, exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return "", fmt.Errorf(" » failed to execute command substitution for variable '%s' on line %d in '%s': %w", key, lineNum, envFilePath, err)
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}

// usage prints detailed usage information to stderr and exits the program
// with a non-zero status, indicating an error or invalid invocation.
func usage() {
	fmt.Fprintf(os.Stderr, `Usage: load-env <id>[,<id2>,...] [<executable> [<args...>]]
       load-env <id>[,<id2>,...] --view  (to display variables read from the file(s) and EXIT)
       eval "$(load-env <id>[,<id2>,...] --export)" (to load environment into the current shell)
       load-env --version    (to display version information)
       load-env --help       (to display this help message)

Description:
  Loads environment variables from one or more .env files, specified by comma-separated IDs.
  Files are processed in order, with later files overriding variables from earlier ones.
  load-env looks for <id>.env in the current directory, or if not found,
  from ~/.config/load-env/<id>.env (or the path in LOAD_ENV_CONFIG_DIR).
  Supports variable expansion (e.g., FOO=$BAR or FOO=${BAR}) and command substitution.
  For command substitution, both $(...) and $[...] syntaxes are available.
  The $[...] syntax is recommended for commands that include parentheses or backticks.

Options:
  --sandboxed       If set, the executed command will receive an environment
                    composed *only* of variables defined in the .env files,
                    disregarding any inherited environment variables not
                    explicitly overridden. By default, inherited variables
                    are included and overridden by .env file definitions.
                    Example: load-env myproject --sandboxed bash -c export

Modes of Operation:
  1. load-env <id>[,<id2>,...] <executable> [args...]
     Loads variables from the specified .env file(s), then runs <executable> with its arguments.
     Variables are isolated to the executable's process and do not persist.
     Example: load-env common,dev -- go run main.go

  2. load-env <id>[,<id2>,...]
     Loads variables from the specified .env file(s), then launches a new interactive subshell (default: %s).
     Variables are isolated to the subshell and do not persist after exiting it.
     Example: load-env base,project_secrets

  3. eval "$(load-env <id>[,<id2>,...] --export)"
     Loads variables from the specified .env file(s) and prints 'export' commands to stdout.
     These commands must be evaluated in your CURRENT shell session (e.g., using 'eval').
     Variables WILL persist. Use with caution for sensitive data (visible via 'ps e').
     Example: eval "$(load-env common,prod --export)"

  4. load-env <id>[,<id2>,...] --view
     Displays the resolved variables (including secrets from command substitutions) that would be loaded.
     WARNING: This will print plaintext secrets to your terminal. Use with caution.
     This mode does NOT launch a shell or run an executable; it only displays.
     Example: load-env local_dev_secrets --view

Environment File Format:
  (Looked for in current directory first, then in ~/.config/load-env/)
  KEY=VALUE
  # Comments are supported
  DB_PASS=$(gopass show myproject/database/password) # Special command substitution: supports 'gopass show <path>' or 'gopass <path>'
  MY_SECRET=$(some_simple_cmd)                       # Generic command substitution with $() syntax (use with caution for complex commands)
  API_KEY=$[retrieve-api-key.sh --key=abc]           # Robust command substitution using $[] syntax (recommended for complexity)
  # Example of $[] handling internal parentheses/backticks:
  # COMPLEX_CMD=$[echo "Current time is $(date) (GMT)"]
  APP_PORT=8080
  API_URL=http://localhost:$APP_PORT # Variable expansion example
  SECRET_MESSAGE="Hello \"world\""   # Double-quoted value with inner escapes
  LITERAL_STRING='This is a literal string with $ and \' characters' # Single-quoted value

`, defaultShell) // Note: Using defaultShell here for the %s in Mode 2 description.
	os.Exit(1)
}

// expandVarsInString performs variable expansion on a given string using the provided environment map.
// It replaces `$VAR` or `${VAR}` patterns with their values.
func expandVarsInString(text string, combinedEnvForLookup map[string]string) string {
	return variableExpansionRegex.ReplaceAllStringFunc(text, func(matchStr string) string {
		varName := ""
		matches := variableExpansionRegex.FindStringSubmatch(matchStr)
		if len(matches) > 1 && matches[1] != "" { // $VAR format (group 1)
			varName = matches[1]
		} else if len(matches) > 2 && matches[2] != "" { // ${VAR} format (group 2)
			varName = matches[2]
		}
		if val, ok := combinedEnvForLookup[varName]; ok {
			return val
		}
		// If variable is not found, expand to an empty string (standard behavior)
		return ""
	})
}

// parseEnvFile reads the .env file at the given path, processes each line
// for key-value pairs, handles command substitutions, unquotes values,
// and finally performs variable expansion. It returns a map of the fully
// resolved environment variables that were *defined in the .env file*.
func parseEnvFile(envFilePath string, cmdExecutor commandExecutor, inheritedEnvMap map[string]string) (map[string]string, error) {
	file, err := os.Open(envFilePath)
	if err != nil {
		return nil, fmt.Errorf(" » could not open .env file '%s': %w", envFilePath, err)
	}
	defer file.Close() // Ensure the file is closed when the function exits.

	initialEnvMap := make(map[string]string) // Stores only fully resolved values.
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text()) // Read and trim whitespace from the line.

		// Skip empty lines and lines that are comments (start with '#').
		if len(line) == 0 || strings.HasPrefix(line, "#") {
			continue
		}

		// Split the line into a key and a value at the first '=' sign.
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			// If a line doesn't contain an '=', it's considered malformed.
			// Print a warning to stderr and skip this line.
			fmt.Fprintf(os.Stderr, " » load-env: Warning: Skipping malformed line %d in '%s': '%s'. Expected 'KEY=VALUE' format.\n", lineNum, envFilePath, line)
			continue
		}

		// Ensure no leading or trailing spaces
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Handle quoted values:
		// Double-quoted strings support escape sequences (processed by strconv.Unquote).
		if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) && len(value) >= 2 {
			unquotedValue, err := strconv.Unquote(value)
			if err == nil {
				value = unquotedValue // If unquoting is successful, use the unquoted value.
			} else {
				// If unquoting fails (e.g., malformed escape, unclosed quote),
				// log a warning and fall back to simply stripping the outer quotes.
				fmt.Fprintf(os.Stderr, " » load-env: Warning: Could not fully unquote value '%s' on line %d in '%s'. Error: %v. Using value after simple outer quote stripping.\n", value, lineNum, envFilePath, err)
				value = value[1 : len(value)-1] // Strip outer quotes manually.
			}
		} else if strings.HasPrefix(value, `'`) && strings.HasSuffix(value, `'`) && len(value) >= 2 {
			// Single-quoted strings are treated literally; only strip outer quotes.
			// No inner escape sequence processing (e.g., '\' remains '\' inside single quotes).
			value = value[1 : len(value)-1]
		}

		// Handle literal dollar signs: Replace escaped "\$" with literalDollarPlaceholder
		// This must happen after unquoting, but before command and variable expansion,
		// so that `\$` is not misinterpreted as a variable.
		value = strings.ReplaceAll(value, `\$`, literalDollarPlaceholder)

		// Prepare the combined environment map for lookup during *this line's* processing.
		// It includes inherited variables and variables from previously processed lines.
		combinedEnvForLookup := mergeMaps(inheritedEnvMap, initialEnvMap)

		// --- Process Value: Aligned with logic.py's process_value function ---

		// 1. Variable Expansion Pass
		// This replaces `$VAR` or `${VAR}` with their values from combinedEnvForLookup.
		value = expandVarsInString(value, combinedEnvForLookup)
		// No inner loop needed here, as we established (initialEnvMap already has fully resolved values from prior lines)
		// and this ReplaceAllStringFunc will resolve all immediate $VARs.

		// 2. Gopass Command Substitution Pass
		// Replaces `$(gopass show <path>)` with its output.
		value = gopassRegex.ReplaceAllStringFunc(value, func(matchStr string) string {
			matches := gopassRegex.FindStringSubmatch(matchStr)
			if len(matches) < 2 { // Should not happen if regex matched
				return matchStr // Return original if path not captured
			}
			gopassPath := matches[1]
			commandToExecute := fmt.Sprintf("gopass show --password %s", gopassPath)

			output, err := executeCommandSubstitution(key, commandToExecute, envFilePath, lineNum, cmdExecutor, inheritedEnvMap, initialEnvMap)
			if err != nil {
				fmt.Fprintf(os.Stderr, " » load-env: Warning: %v.\n", err)
				fmt.Fprintln(os.Stderr, " » This usually means the gopass secret does not exist or gopass encountered an error. Value set to empty.")
				return ""
			}

			// Crucially: Expand variables *within the command's output*
			output = expandVarsInString(output, combinedEnvForLookup)

			if output == "" {
				fmt.Fprintf(os.Stderr, " » load-env: Warning: gopass command for variable '%s' (path: '%s') returned an empty value on line %d in '%s'.\n", key, gopassPath, lineNum, envFilePath)
			}
			return output
		})

		// 3. Generic Command Substitution Pass
		// Replaces `$[command args...]` with its output.
		value = applyCommandSubstitution(value, alternateCommandRegex, key, envFilePath, lineNum, cmdExecutor, inheritedEnvMap, initialEnvMap, combinedEnvForLookup)
		// Replaces `$(command args...)` with its output.
		value = applyCommandSubstitution(value, genericCommandRegex, key, envFilePath, lineNum, cmdExecutor, inheritedEnvMap, initialEnvMap, combinedEnvForLookup)

		// Store the fully processed (expanded and substituted) key-value pair.
		// initialEnvMap now directly holds the resolved values.
		initialEnvMap[key] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf(" » error reading .env file '%s': %w", envFilePath, err)
	}

	// At this point, initialEnvMap contains all fully resolved values from the .env file.
	return initialEnvMap, nil
}

// mapToSlice converts a map[string]string to a slice of strings in "KEY=VALUE" format.
// It sorts the keys to ensure consistent output order, which is helpful for
// deterministic behavior in `--view` mode and for reliable testing.
func mapToSlice(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys) // Sort keys alphabetically.

	s := make([]string, 0, len(m))
	for _, k := range keys {
		s = append(s, fmt.Sprintf("%s=%s", k, m[k]))
	}
	return s
}

// mergeMaps accepts one or more maps of type map[string]string and merges them
// into a single new map. The order of maps matters: if a key exists in multiple
// input maps, the value from the map appearing later in the argument list will
// take precedence.
func mergeMaps(maps ...map[string]string) map[string]string {
	merged := make(map[string]string)

	for _, m := range maps {
		for k, v := range m {
			merged[k] = v // Assign (or overwrite) the value
		}
	}
	return merged
}

func main() {
	args := os.Args[1:] // Get command-line arguments, excluding the program name itself.

	var (
		sandBoxed  bool     // Flag for `--sandboxed` mode.
		viewMode   bool     // Flag for `--view` mode.
		exportMode bool     // Flag for `--export` mode.
		executable string   // The executable to run in default mode.
		execArgs   []string // Arguments for the executable.
	)

	// --- Parse Command-Line Flags ---
	// Check for global flags like `--version`, `--help`, `--view`, `--export` at the start of args.
	if len(args) > 0 {
		switch args[0] {
		case "--version":
			fmt.Printf("load-env version %s\n", version)
			os.Exit(0) // Exit after printing version.
		case "--help":
			usage() // Print usage and exit.
		case "--view":
			viewMode = true
			if len(args) > 1 { // If ID is provided after --view
				args = []string{args[1]}
			} else { // If only --view is provided without ID
				usage() // ID is mandatory
			}
		case "--export":
			exportMode = true
			if len(args) > 1 { // If ID is provided after --export
				args = []string{args[1]}
			} else { // If only --export is provided without ID
				usage() // ID is mandatory
			}
		case "--sandboxed":
			sandBoxed = true
			args = args[1:]
		default:
			// Handle cases where flags might appear after the ID (e.g., load-env myid --view)
			if len(args) > 1 {
				switch args[1] {
				case "--view":
					viewMode = true
					args = []string{args[0]} // Keep only the ID
				case "--export":
					exportMode = true
					args = []string{args[0]} // Keep only the ID
				case "--sandboxed":
					sandBoxed = true
					if len(args) > 2 { // load-env ID --sandboxed executable args
						args = append(args[:1], args[2:]...) // Remove --sandboxed, keep ID and subsequent args
					} else { // load-env ID --sandboxed
						args = args[:1] // Keep only ID
					}
				}
			}
		}
	}

	// --- Validate Remaining Arguments ---
	if len(args) == 0 {
		// If no ID is provided after flag parsing, display usage and exit.
		usage()
	}

	idsStr := args[0] // The first remaining argument is always the ID for the .env file.
	if len(args) > 1 {
		executable = args[1] // The second argument (if present) is the executable.
		execArgs = args[1:]  // All arguments from the executable onwards are its arguments.
	}

	// Split the comma-delimited IDs
	envIDs := strings.Split(idsStr, ",")
	if len(envIDs) == 0 || (len(envIDs) == 1 && strings.TrimSpace(envIDs[0]) == "") {
		fmt.Fprintln(os.Stderr, " » Error: No .env file IDs provided.")
		os.Exit(1)
	}

	var envFilePath string
	var envFilePaths []string

	for _, envID := range envIDs {
		envID = strings.TrimSpace(envID) // Clean up potential spaces from comma split
		if envID == "" {
			continue // Skip empty parts if user provides "id1,,id2"
		}

		localEnvFileName := envID + ".env"

		// 1. Try to find the .env file in the current directory first.
		if _, err := os.Stat(localEnvFileName); err == nil {
			envFilePath = localEnvFileName
		} else if os.IsNotExist(err) {
			// 2. If not found in the current directory, then check the configured directory.
			configDir := os.Getenv("LOAD_ENV_CONFIG_DIR")
			if configDir == "" {
				homeDir, err := os.UserHomeDir()
				if err != nil {
					fmt.Fprintf(os.Stderr, " » load-env: Error: Could not determine user home directory: %v\n", err)
					os.Exit(1)
				}
				configDir = filepath.Join(homeDir, DefaultConfigDir)
			}
			envFilePath = filepath.Join(configDir, localEnvFileName)

			// Check if the file exists in the configured directory.
			if _, err := os.Stat(envFilePath); os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, " » load-env: Error: Environment file '%s' not found in current directory or '%s'.\n", localEnvFileName, configDir)
				os.Exit(1)
			} else if err != nil {
				fmt.Fprintf(os.Stderr, " » load-env: Error: Could not access environment file '%s': %v\n", envFilePath, err)
				os.Exit(1)
			}
		} else {
			// An error other than "not exist" occurred when checking the current directory.
			fmt.Fprintf(os.Stderr, " » load-env: Error: Could not access environment file '%s' in current directory: %v\n", localEnvFileName, err)
			os.Exit(1)
		}
		envFilePaths = append(envFilePaths, envFilePath)
	}

	// Initialize the environment map with the current process's environment.
	osEnvMap := make(map[string]string)
	for _, envVar := range os.Environ() {
		parts := strings.SplitN(envVar, "=", 2)
		if len(parts) == 2 {
			osEnvMap[parts[0]] = parts[1]
		}
	}

	jointResolvedEnvMap := make(map[string]string)
	inheritedEnvMap := osEnvMap

	for _, envFilePath := range envFilePaths {
		// --- Parse and Resolve Environment Variables (common step for all modes) ---
		// `parseEnvFile` returns a `map[string]string` containing the fully resolved variables.
		resolvedEnvMap, err := parseEnvFile(envFilePath, defaultCommandExecutor, inheritedEnvMap)
		if err != nil {
			fmt.Fprintf(os.Stderr, " » load-env: Error parsing .env file: %v\n", err)
			os.Exit(1)
		}
		// Merge the resolved variables from the current .env file into the joint map.
		// Later files override earlier ones.
		jointResolvedEnvMap = mergeMaps(jointResolvedEnvMap, resolvedEnvMap)

		// For processing the *next* .env file in the chain, the `inheritedEnvMap`
		// should be the combination of `osEnvMap` and all files processed so far.
		inheritedEnvMap = mergeMaps(osEnvMap, jointResolvedEnvMap)
	}

	// Final pass to replace the placeholder for literal dollar signs ($) that were escaped.
	for k, v := range jointResolvedEnvMap {
		jointResolvedEnvMap[k] = strings.ReplaceAll(v, literalDollarPlaceholder, `$`)
	}

	// Convert the resolved map back to a slice of "KEY=VALUE" strings.
	// This format is required for `syscall.Exec` and convenient for `--export` and `--view` modes.
	jointResolvedEnvVars := mapToSlice(jointResolvedEnvMap)

	// --- Execute based on the determined mode ---
	if viewMode {
		// Mode 4: `--view` (Display variables and then EXIT).
		for _, varPair := range jointResolvedEnvVars {
			// Split KEY=VALUE to display in a user-friendly KEY="VALUE" format.
			parts := strings.SplitN(varPair, "=", 2)
			if len(parts) == 2 {
				// Use `%q` to properly quote the value for display, similar to bash's `printf %q`.
				fmt.Printf("%s=%q\n", parts[0], parts[1])
			} else {
				// Fallback for malformed pairs, though `mapToSlice` should prevent this.
				fmt.Println(varPair)
			}
		}
		os.Exit(0) // Exit after displaying variables.
	} else if exportMode {
		// Mode 3: Load into current shell (via `eval "$(load-env --export <id>)"`).
		for _, varPair := range jointResolvedEnvVars {
			parts := strings.SplitN(varPair, "=", 2)
			if len(parts) == 2 {
				// Print `export` commands with proper quoting for the shell to evaluate.
				fmt.Printf("export %s=%q\n", parts[0], parts[1])
			} else {
				// Fallback, should not be hit with current parsing.
				fmt.Printf("export %s\n", parts[0])
			}
		}
		// DO NOT `os.Exit(0)` here. The output of this program is intended to be evaluated
		// by the calling shell, and a non-zero exit could abort the `eval` command.
	} else {
		// Mode 1 or 2: Run specified executable or launch a default subshell.
		targetCmd := ""
		if executable != "" {
			// Mode 1: Run a specific executable.
			targetCmd = executable
			if strings.HasPrefix(targetCmd, "-") {
				fmt.Fprintf(os.Stderr, " » load-env: Invalid option: %s\n", targetCmd)
				os.Exit(1)
			}
		} else {
			// Mode 2: Launch a default interactive subshell.
			targetCmd = os.Getenv("SHELL") // Use user's preferred shell if set.
			if targetCmd == "" {
				targetCmd = defaultShell // Fallback to 'bash'.
			}
			fmt.Fprintf(os.Stderr, " » load-env: Launching new '%s' subshell with environment for '%s'...\n", targetCmd, idsStr)
			execArgs = []string{targetCmd, "-i"} // `-i` makes the shell interactive.
		}

		// Attempt to find the absolute path of the target command in the system's PATH.
		absTargetCmd, err := exec.LookPath(targetCmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, " » load-env: Error: Executable '%s' not found in PATH: %v\n", targetCmd, err)
			os.Exit(1)
		}

		// Prepare arguments for `syscall.Exec`. The first argument in `finalArgs`
		// must be the program name itself (conventionally, argv[0]).
		var finalArgs []string
		if executable != "" {
			finalArgs = execArgs // If an executable was given, its args start from executable name.
		} else {
			finalArgs = execArgs // If subshell, `execArgs` already contains `targetCmd` (shell) and `-i`.
		}

		var envp []string
		if sandBoxed {
			// Convert the joint resolved map back to a slice for `syscall.Exec`.
			envp = mapToSlice(jointResolvedEnvMap)
		} else {
			// Prepare the full set of environment variables to pass to the executable
			fullSetEnvMap := mergeMaps(osEnvMap, jointResolvedEnvMap)

			// Convert to a slice for `syscall.Exec`.
			envp = mapToSlice(fullSetEnvMap)
		}

		// Perform `syscall.Exec`. This replaces the current `load-env` Go process
		// with the target command, passing the merged environment and arguments.
		// `syscall.Exec` is a low-level call, typically used for this purpose on Unix-like systems.
		// If `syscall.Exec` returns, it means it failed to execute the command.
		err = syscall.Exec(absTargetCmd, finalArgs, envp)
		if err != nil {
			fmt.Fprintf(os.Stderr, " » load-env: Error executing '%s': %v\n", absTargetCmd, err)
			os.Exit(1)
		}
		// Code after `syscall.Exec` will only run if `syscall.Exec` failed.
	}
}
