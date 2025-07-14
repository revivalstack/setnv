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
	version = "0.1.0" // Current version of the tool

	// DefaultConfigDir defines the default centralized directory for .env files.
	// It's relative to the user's home directory (e.g., ~/.config/load-env).
	// This can be overridden by the LOAD_ENV_CONFIG_DIR environment variable.
	DefaultConfigDir = ".config/load-env"

	// defaultShell is the shell to launch when `load-env <id>` is called
	// without a specified executable.
	defaultShell = "bash"
)

// commandExecutor is a type that represents a function capable of executing a command.
// This abstraction is crucial for testing, allowing us to easily mock `os/exec.Command`
// without actually running external processes during tests.
type commandExecutor func(name string, arg ...string) *exec.Cmd

// defaultCommandExecutor is the actual `os/exec.Command` function used in production.
var defaultCommandExecutor commandExecutor = exec.Command

// usage prints detailed usage information to stderr and exits the program
// with a non-zero status, indicating an error or invalid invocation.
func usage() {
	fmt.Fprintf(os.Stderr, `Usage: load-env <id> [<executable> [<args...>]]
       load-env --view <id>  (to display variables read from the file and EXIT)
       eval "$(load-env --export <id>)" (to load environment into the current shell)
       load-env --version    (to display version information)
       load-env --help       (to display this help message)

Description:
  Loads environment variables from <id>.env in the current directory, or if not found,
  from ~/.config/load-env/<id>.env (or the path in LOAD_ENV_CONFIG_DIR).
  Supports variable expansion (e.g., FOO=$BAR or FOO=${BAR}) and gopass substitution.

Modes of Operation:
  1. load-env <id> <executable> [args...]
     Loads variables, then runs <executable> with its arguments.
     Variables are isolated to the executable's process and do not persist.

  2. load-env <id>
     Loads variables, then launches a new interactive subshell (default: %s).
     Variables are isolated to the subshell and do not persist after exiting it.

  3. eval "$(load-env --export <id>)"
     Loads variables and prints 'export' commands to stdout.
     These commands must be evaluated in your CURRENT shell session (e.g., using 'eval').
     Variables WILL persist. Use with caution for sensitive data (visible via 'ps e').

  4. load-env --view <id>
     Displays the resolved variables (including gopass secrets) that would be loaded.
     WARNING: This will print plaintext secrets to your terminal. Use with caution.
     This mode does NOT launch a shell or run an executable; it only displays.

Environment File Format:
  (Looked for in current directory first, then in ~/.config/load-env/)
  KEY=VALUE
  # Comments are supported
  DB_PASS=$(gopass show myproject/database/password)
  APP_PORT=8080
  API_URL=http://localhost:$APP_PORT # Variable expansion example
  SECRET_MESSAGE="Hello \"world\""   # Double-quoted value with inner escapes
  LITERAL_STRING='This is a literal string with $ and \' characters' # Single-quoted value

`, DefaultConfigDir)
	os.Exit(1)
}

// parseEnvFile reads the .env file at the given path, processes each line
// for key-value pairs, handles gopass command substitutions, unquotes values,
// and finally performs variable expansion. It returns a map of the fully
// resolved environment variables that were *defined in the .env file*.
func parseEnvFile(envFilePath string, cmdExecutor commandExecutor) (map[string]string, error) {
	file, err := os.Open(envFilePath)
	if err != nil {
		return nil, fmt.Errorf("could not open .env file '%s': %w", envFilePath, err)
	}
	defer file.Close() // Ensure the file is closed when the function exits.

	initialEnvMap := make(map[string]string) // Stores parsed values before variable expansion.
	scanner := bufio.NewScanner(file)
	lineNum := 0

	// gopassRegex identifies `$(gopass show <path>)` patterns for command substitution.
	gopassRegex := regexp.MustCompile(`^\$\(gopass\ show\ (.*)\)$`)

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
			fmt.Fprintf(os.Stderr, "load-env: Warning: Skipping malformed line %d in '%s': '%s'. Expected 'KEY=VALUE' format.\n", lineNum, envFilePath, line)
			continue
		}

		key := strings.TrimSpace(parts[0]) // Key is always trimmed.
		value := parts[1]                  // Value might contain quotes or leading/trailing spaces.

		// Handle quoted values:
		// Double-quoted strings support escape sequences (processed by strconv.Unquote).
		if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) && len(value) >= 2 {
			unquotedValue, err := strconv.Unquote(value)
			if err == nil {
				value = unquotedValue // If unquoting is successful, use the unquoted value.
			} else {
				// If unquoting fails (e.g., malformed escape, unclosed quote),
				// log a warning and fall back to simply stripping the outer quotes.
				fmt.Fprintf(os.Stderr, "load-env: Warning: Could not fully unquote value '%s' on line %d in '%s'. Error: %v. Using value after simple outer quote stripping.\n", value, lineNum, envFilePath, err)
				value = value[1 : len(value)-1] // Strip outer quotes manually.
			}
		} else if strings.HasPrefix(value, `'`) && strings.HasSuffix(value, `'`) && len(value) >= 2 {
			// Single-quoted strings are treated literally; only strip outer quotes.
			// No inner escape sequence processing (e.g., '\' remains '\' inside single quotes).
			value = value[1 : len(value)-1]
		}

		// Handle literal dollar signs: Replace escaped "\$" with "$"
		// This must happen after unquoting, but before gopass and variable expansion,
		// so that `\$` is not misinterpreted as a variable.
		value = strings.ReplaceAll(value, `\$`, `$`)

		// Process gopass command substitution (e.g., `DB_PASS=$(gopass show path)`).
		if matches := gopassRegex.FindStringSubmatch(value); len(matches) > 1 {
			gopassPath := matches[1] // Extract the gopass path from the regex match.
			// Execute `gopass show --password <path>`.
			cmd := cmdExecutor("gopass", "show", "--password", gopassPath)
			cmd.Stderr = os.Stderr // Direct gopass's stderr to `load-env`'s stderr for visibility.
			gopassOutput, err := cmd.Output()
			if err != nil {
				// If gopass fails, log a warning and set the variable's value to empty.
				fmt.Fprintf(os.Stderr, "load-env: Warning: gopass command for variable '%s' (path: '%s') failed on line %d in '%s'. Error: %v\n", key, gopassPath, lineNum, envFilePath, err)
				fmt.Fprintln(os.Stderr, "  This usually means the secret does not exist or gopass encountered an error. Value set to empty.")
				value = ""
			} else {
				// On success, trim the trailing newline from gopass output.
				value = strings.TrimSuffix(string(gopassOutput), "\n")
				if value == "" {
					// Warn if gopass returned an empty value.
					fmt.Fprintf(os.Stderr, "load-env: Warning: gopass command for variable '%s' (path: '%s') returned an empty value on line %d in '%s'.\n", key, gopassPath, lineNum, envFilePath)
				}
			}
		}

		// Store the processed key-value pair in the initial map.
		initialEnvMap[key] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading .env file '%s': %w", envFilePath, err)
	}

	// Prepare the combined environment map for variable lookup during expansion.
	// This map combines system environment variables with those initially parsed
	// from the .env file. Variables from the .env file will take precedence
	// during lookup if their keys conflict with existing system variables.
	combinedEnvForLookup := make(map[string]string)
	for _, envVar := range os.Environ() { // Start with current system environment
		parts := strings.SplitN(envVar, "=", 2)
		if len(parts) == 2 {
			combinedEnvForLookup[parts[0]] = parts[1]
		}
	}
	// Variables parsed directly from the .env file override system variables for lookup.
	// This ensures that an .env variable can refer to another .env variable,
	// or override an existing system variable and then be referenced.
	for k, v := range initialEnvMap {
		combinedEnvForLookup[k] = v
	}

	// Perform iterative variable expansion on the variables *that came from the .env file*.
	// Use the `combinedEnvForLookup` map for resolving any references.
	resolvedEnvMap := expandVariables(initialEnvMap, combinedEnvForLookup)

	return resolvedEnvMap, nil
}

// variableExpansionRegex is a regular expression to find occurrences of
// `$VAR` or `${VAR}` patterns within a string.
// Group 1 captures the variable name for `$VAR` (e.g., `VAR_NAME`).
// Group 2 captures the variable name for `${VAR}` (e.g., `VAR_NAME`).
var variableExpansionRegex = regexp.MustCompile(`\$(?:([a-zA-Z_][a-zA-Z0-9_]*)|{([a-zA-Z_][a-zA-Z0-9_]*)})`)

// expandVariables performs iterative variable expansion on values within the provided map.
// It resolves references like `$VAR` or `${VAR}` using other variables defined
// within the same map. It includes safeguards against infinite loops from
// circular dependencies and ensures unresolvable variables are set to empty strings.
func expandVariables(varsToExpand map[string]string, baseEnv map[string]string) map[string]string {
	// Create a copy of varsToExpand to work on; we'll modify its values.
	expandedVars := make(map[string]string, len(varsToExpand))
	for k, v := range varsToExpand {
		expandedVars[k] = v
	}

	// currentLookupEnv will be used for looking up variable values during expansion.
	// It starts with the baseEnv (current shell + .env's initial values)
	// and gets updated with `expandedVars` as they resolve.
	currentLookupEnv := make(map[string]string, len(baseEnv) + len(expandedVars))
	for k, v := range baseEnv {
		currentLookupEnv[k] = v
	}
	// Initially, .env vars (even if not yet fully expanded) take precedence for lookup
	// within the combined environment.
	for k, v := range expandedVars {
		currentLookupEnv[k] = v
	}

	maxIterations := 10 // Maximum iterations to attempt expansion; guards against circular deps.

	for i := 0; i < maxIterations; i++ {
		currentPassChanged := false // Flag to track if any variable was expanded in the current pass.

		// Before each pass, ensure `currentLookupEnv` reflects the latest
		// state of `expandedVars` for accurate lookups.
		for k, v := range expandedVars {
			currentLookupEnv[k] = v
		}

		for k, v := range expandedVars { // Iterate through the variables we are expanding
			// Replace all found variable patterns in the current value `v`.
			newValue := variableExpansionRegex.ReplaceAllStringFunc(v, func(match string) string {
				varName := ""
				matches := variableExpansionRegex.FindStringSubmatch(match)

				// Determine which capture group holds the actual variable name.
				if len(matches) > 1 && matches[1] != "" {
					varName = matches[1] // Matched $VAR format.
				} else if len(matches) > 2 && matches[2] != "" {
					varName = matches[2] // Matched ${VAR} format.
				}

				// Look up the variable name in our combined lookup environment.
				if val, ok := currentLookupEnv[varName]; ok { // KEY CHANGE: Lookup in currentLookupEnv
					return val // Replace the pattern with its resolved value.
				}
				// If the referenced variable is not found, it expands to an empty string.
				return ""
			})

			// If the variable's value changed after this pass of expansion,
			// update the map and mark that a change occurred.
			if newValue != v {
				expandedVars[k] = newValue
				currentPassChanged = true
			}
		}

		// If no variables were changed in this entire pass, it means all possible
		// expansions have occurred, and the map has stabilized. We can break early.
		if !currentPassChanged {
			break
		}
	}

	// Post-expansion cleanup for unresolvable variables (e.g., true circular dependencies).
	// After all iterations, if any variable still contains an expansion pattern,
	// it means it could not be resolved (e.g., `A=$B` and `B=$A`).
	// In such cases, set the variable to an empty string and issue a warning.
	unresolvedDetected := false
	for k, v := range expandedVars {
		if variableExpansionRegex.MatchString(v) {
			expandedVars[k] = "" // Resolve the unresolvable variable to an empty string.
			unresolvedDetected = true
		}
	}

	// If any unresolvable variables were detected and cleaned up, issue a warning.
	if unresolvedDetected {
		fmt.Fprintf(os.Stderr, "load-env: Warning: Variable expansion did not fully resolve after iterations. Possible circular dependencies or unresolvable variables. Remaining references resolved to empty strings.\n")
	}

	return expandedVars
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

func main() {
	args := os.Args[1:] // Get command-line arguments, excluding the program name itself.

	var (
		viewMode   bool   // Flag for `--view` mode.
		exportMode bool   // Flag for `--export` mode.
		id         string // Identifier for the .env file (e.g., "myproject").
		executable string // The executable to run in default mode.
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
			args = args[1:] // Consume the `--view` flag.
		case "--export":
			exportMode = true
			args = args[1:] // Consume the `--export` flag.
		}
	}

	// --- Validate Remaining Arguments ---
	if len(args) == 0 {
		// If no ID is provided after flag parsing, display usage and exit.
		usage()
	}

	id = args[0] // The first remaining argument is always the ID for the .env file.
	if len(args) > 1 {
		executable = args[1] // The second argument (if present) is the executable.
		execArgs = args[1:]  // All arguments from the executable onwards are its arguments.
	}

	var envFilePath string
	localEnvFileName := id + ".env"

	// 1. Try to find the .env file in the current directory first.
	if _, err := os.Stat(localEnvFileName); err == nil {
		envFilePath = localEnvFileName
	} else if os.IsNotExist(err) {
		// 2. If not found in the current directory, then check the configured directory.
		configDir := os.Getenv("LOAD_ENV_CONFIG_DIR")
		if configDir == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				fmt.Fprintf(os.Stderr, "load-env: Error: Could not determine user home directory: %v\n", err)
				os.Exit(1)
			}
			configDir = filepath.Join(homeDir, DefaultConfigDir)
		}
		envFilePath = filepath.Join(configDir, localEnvFileName)

		// Check if the file exists in the configured directory.
		if _, err := os.Stat(envFilePath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "load-env: Error: Environment file '%s' not found in current directory or '%s'.\n", localEnvFileName, configDir)
			os.Exit(1)
		} else if err != nil {
			fmt.Fprintf(os.Stderr, "load-env: Error: Could not access environment file '%s': %v\n", envFilePath, err)
			os.Exit(1)
		}
	} else {
		// An error other than "not exist" occurred when checking the current directory.
		fmt.Fprintf(os.Stderr, "load-env: Error: Could not access environment file '%s' in current directory: %v\n", localEnvFileName, err)
		os.Exit(1)
	}

	// --- Parse and Resolve Environment Variables (common step for all modes) ---
	// `parseEnvFile` returns a `map[string]string` containing the fully resolved variables.
	resolvedEnvMap, err := parseEnvFile(envFilePath, defaultCommandExecutor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load-env: Error parsing .env file: %v\n", err)
		os.Exit(1)
	}

	// Convert the resolved map back to a slice of "KEY=VALUE" strings.
	// This format is required for `syscall.Exec` and convenient for `--export` and `--view` modes.
	resolvedEnvVars := mapToSlice(resolvedEnvMap)

	// --- Execute based on the determined mode ---
	if viewMode {
		// Mode 4: `--view` (Display variables and then EXIT).
		for _, varPair := range resolvedEnvVars {
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
		if executable != "" {
			// In `--export` mode, no executable or arguments should follow the ID.
			fmt.Fprintf(os.Stderr, "load-env: Error: When using --export, no executable or arguments should be provided (found: '%s').\n", executable)
			fmt.Fprintf(os.Stderr, "       Did you mean to run 'load-env %s %s' instead of 'eval \"$(load-env --export %s %s)\"'?\n", id, strings.Join(execArgs, " "), id, strings.Join(execArgs, " "))
			os.Exit(1)
		}
		for _, varPair := range resolvedEnvVars {
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
		} else {
			// Mode 2: Launch a default interactive subshell.
			targetCmd = os.Getenv("SHELL") // Use user's preferred shell if set.
			if targetCmd == "" {
				targetCmd = defaultShell // Fallback to 'bash'.
			}
			fmt.Fprintf(os.Stderr, "load-env: Launching new '%s' subshell with environment for '%s'...\n", targetCmd, id)
			execArgs = []string{targetCmd, "-i"} // `-i` makes the shell interactive.
		}

		// Attempt to find the absolute path of the target command in the system's PATH.
		absTargetCmd, err := exec.LookPath(targetCmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load-env: Error: Executable '%s' not found in PATH: %v\n", targetCmd, err)
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

		// Merge current environment variables with the newly resolved ones.
		// New variables from the .env file will override existing ones with the same key.
		currentEnv := os.Environ()
		mergedEnvMap := make(map[string]string)
		for _, envVar := range currentEnv {
			parts := strings.SplitN(envVar, "=", 2)
			if len(parts) == 2 {
				mergedEnvMap[parts[0]] = parts[1]
			}
		}
		for k, v := range resolvedEnvMap {
			mergedEnvMap[k] = v // New variables take precedence.
		}
		// Convert the merged map back to a slice for `syscall.Exec`.
		envp := mapToSlice(mergedEnvMap)

		// Perform `syscall.Exec`. This replaces the current `load-env` Go process
		// with the target command, passing the merged environment and arguments.
		// `syscall.Exec` is a low-level call, typically used for this purpose on Unix-like systems.
		// If `syscall.Exec` returns, it means it failed to execute the command.
		err = syscall.Exec(absTargetCmd, finalArgs, envp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load-env: Error executing '%s': %v\n", absTargetCmd, err)
			os.Exit(1)
		}
		// Code after `syscall.Exec` will only run if `syscall.Exec` failed.
	}
}