# load-env: Your .env files, supercharged.

> Effortless environment variable loading and management for any workflow.

Manage your application's environment variables with simplicity and power. `load-env` helps you centralize configuration and sensitive data in easy-to-use `.env` files, supporting dynamic variable expansion and seamless secret integration.

## Quick Example

Store your kopia environment variables in a `myconfig.env` file, and use `load-env` to call kopia with the resolved environment variables including gopass secrets.

```
load-env myconfig kopia <params>
```

- `load-env` looks for a `myconfig.env` file in current directory, or in `~/.config/load-env/myconfig.env`
- reads `myconfig.env` file and loads `KEY=VALUE` pairs, e.g. `KOPIA_PASSWORD=$(gopass show test/KOPIA_PASSWORD)`
- pass these resolved `KEY=VALUE` pairs to `kopia <params>`

## Features

- **Intelligent `.env` Parsing**: Reads `KEY=VALUE` pairs, gracefully skipping comments and empty lines.
- **Smart Value Handling**: Supports both double-quoted values (with full escape sequence support like `\n`, `\"`) and literal single-quoted values.
- **Robust Variable Expansion**: Resolve `$VAR` and `${VAR}` references within your `.env` file. It handles recursive expansions and prevents infinite loops from circular dependencies, resolving unresolvable variables to empty strings with a warning.
- **Seamless Gopass Integration**: Directly inject secrets from your `gopass` store using `$(gopass show <path>)` syntax, keeping sensitive data out of plain text.
- **Command Substitution**: Execute shell commands and use their standard output as variable values using `$(command args)`. This works for any command, e.g. `MY_VAR=$(echo "hello")`, `DB_PASS=$(gopass show my/db/pass)`.
- **Flexible Execution Modes**:

  - **Execute a Command**: Load variables and run a specified executable with its arguments, with the environment isolated to that process.

  - **Launch a Subshell**: Load variables and open a new interactive shell, handy for development sessions.

  - **Export to Current Shell**: Generate `export` commands for `eval`ing the environment directly into your active shell session.

  - **View Variables**: Safely display the fully resolved environment variables before applying them, useful for debugging.

- **Portable & Minimal**: Built in Go, `load-env` compiles into a single, self-contained binary, ensuring easy distribution and minimal external dependencies.

## How Does It Work?

`load-env` first locates your `.env` file, prioritizing the current directory before checking a central configuration directory (e.g., `~/.config/load-env/`). It then meticulously reads each line, parsing key-value pairs, handling quoted strings, and executing `gopass` commands to fetch secrets.

After the initial parsing, `load-env` performs an iterative variable expansion process, resolving all `$VAR` and `${VAR}` references within the loaded environment variables. Finally, based on your chosen mode:

- For running executables or launching subshells, `load-env` uses `syscall.Exec` to replace its own process with the target command, ensuring the environment is seamlessly passed.
- For exporting variables, it prints shell-compatible `export` commands to standard output.
- For viewing, it simply prints the resolved variables to your terminal.

## Why `load-env`?

While other tools exist for managing `.env` files, `load-env` aims to be a minimal, self-contained Go binary that's easy to distribute and use, especially in environments where installing Python or Node.js dependencies might be cumbersome. It provides built-in `gopass` integration for those who manage secrets with it, and clear modes of operation for various workflows.

---

## Installation

You can build `load-env` from source:

```bash
git clone https://github.com/revivalstack/load-env.git
cd load-env
go build -o load-env .
sudo mv load-env /usr/local/bin/
```

or via

```
go install github.com/revivalstack/load-env@latest
```

## Usage

Environment files are expected to be located in `~/.config/load-env/<id>.env`.
You can override the base configuration directory by setting the `LOAD_ENV_CONFIG_DIR` environment variable.

### Basic `.env` File Example (`~/.config/load-env/myproject.env`)

```Code snippet
# My project's environment variables
DB_HOST=localhost
DB_PORT=5432
DB_USER=admin
DB_PASS=$(gopass show myproject/database/password) # Fetches password from gopass
API_KEY="supersecret_key_with_\"quotes\""
APP_URL=http://$DB_HOST:$DB_PORT/app
MESSAGE='This is a literal string with $ and \' characters.'
```

### Running an Executable

To load variables for `myproject` and then run a command:

```Bash
load-env myproject node index.js --port 3000
```

### Launching a Subshell

To load variables for `myproject` and launch a new interactive shell:

```Bash
load-env myproject
```

### Exporting to Current Shell

To load variables for `myproject` into your current shell session (variables will persist):

```Bash
eval "$(load-env --export myproject)"
```

**Warning**: Variables exported this way are visible in your shell's environment (`env`, `ps e`) and can persist across commands. Use with caution for sensitive data.

### Viewing Variables

To see the resolved environment variables for `myproject` without running any commands:

```Bash
load-env --view myproject
```

**Warning**: This will print plaintext secrets (if any are resolved from `gopass`) to your terminal.

### Version and Help

```Bash
load-env --version
load-env --help
```

## Contributing

We welcome contributions to `load-env`! If you have ideas for new features, bug fixes, or improvements, please feel free to open an issue or submit a pull request.

To contribute:

1. Fork the repository.
2. Create a new branch for your feature or bug fix.
3. Make your changes and write tests.
4. Ensure all tests pass (`go test -v`).
5. Submit a pull request.

## Code of Conduct

We strive to create a welcoming and inclusive community. Please be respectful and considerate in all your interactions. We expect all contributors to adhere to basic principles of polite and constructive communication. Any behavior that is harassing, discriminatory, or disruptive is not tolerated. Let's keep `load-env` a friendly place for everyone.

## License

`load-env` is released under the [MIT License](https://www.google.com/search?q=LICENSE).
