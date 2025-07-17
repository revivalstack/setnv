# load-env: Your .env files, supercharged.

> Effortless environment variable loading and management for any workflow.

Manage your application's environment variables with simplicity and power. `load-env` helps you centralize configuration and sensitive data in easy-to-use `.env` files, supporting dynamic variable expansion and seamless secret integration. It also supports chaining multiple `.env` files, allowing for layered configurations.

## Quick Example

Store your kopia environment variables in a `myconfig.env` file, and use `load-env` to call kopia with the resolved environment variables including gopass secrets.

```bash
load-env myconfig kopia [kopia-params]
```

- `load-env` looks for a `myconfig.env` file in current directory, or in `~/.config/load-env/myconfig.env`
- reads `myconfig.env` file and loads `KEY=VALUE` pairs, e.g. `KOPIA_PASSWORD=$(gopass show test/KOPIA_PASSWORD)`
- pass these resolved `KEY=VALUE` pairs to `kopia <params>`

For layered configurations, you can chain multiple `.env` files. For example, `default.env` defines default settings, and `myconfig.env` overrides some variables in the default:

```bash
load-env default,myconfig kopia [kopia-params]
```

## Features

- **Chained Configuration**: Specify multiple `.env` files (e.g., `id1,id2,id3`). Variables from later files in the chain override those from earlier ones, enabling powerful layered configurations.
- **Intelligent `.env` Parsing**: Reads `KEY=VALUE` pairs, gracefully skipping comments and empty lines.
- **Smart Value Handling**: Supports both double-quoted values (with full escape sequence support like `\n`, `\"`) and literal single-quoted values.
- **Robust Variable Expansion**: Resolve `$VAR` and `${VAR}` references within your `.env` file. It handles recursive expansions and prevents infinite loops from circular dependencies, resolving unresolvable variables to empty strings with a warning.
- **Seamless Gopass Integration**: Directly inject secrets from your `gopass` store using `$(gopass show <path>)` or `$(gopass <path>)` syntax. This is a **specially handled command substitution** for convenient secret retrieval, keeping sensitive data out of plain text.
- **Command Substitution**: Dynamically set variable values by executing shell commands and capturing their standard output. `load-env` supports two main syntaxes for general command execution:

  - `$(command args)`: The traditional shell command substitution syntax, e.g., `MY_VAR=$(echo "hello")`. While supported, this syntax has **limitations with complex nested shell syntax** (e.g., unquoted parentheses or backticks) within the command string.

  - `$[command args]`: **Recommended for robust command execution**, especially when your commands include internal parentheses, backticks, or other complex shell constructs. Example: `APP_VERSION=$[git describe --tags --abbrev=0]` or `COMPLEX_CMD=$[echo "Current time is $(date) (GMT)"]`.

- **Flexible Execution Modes**:

  - **Execute a Command**: Load variables and run a specified executable with its arguments, with the environment isolated to that process.

  - **Launch a Subshell**: Load variables and open a new interactive shell, handy for development sessions.

  - **Export to Current Shell**: Generate `export` commands for `eval`ing the environment directly into your active shell session.

  - **View Variables**: Safely display the fully resolved environment variables before applying them, useful for debugging.

- **Environment Sandboxing (`--sandboxed`)**: Control whether `load-env` passes inherited system environment variables to the target process. With `--sandboxed`, only variables explicitly defined in your `.env` files are passed, creating a clean, isolated environment.
- **Portable & Minimal**: Built in Go, `load-env` compiles into a single, self-contained binary, ensuring easy distribution and minimal external dependencies.

## How Does It Work?

`load-env` first locates your `.env` file(s) based on the provided IDs, prioritizing the current directory before checking a central configuration directory (e.g., `~/.config/load-env/`). When chaining multiple IDs (e.g., `base,dev`), files are processed in order, with later files overriding variables defined in earlier ones. It then meticulously reads each line, parsing key-value pairs, handling quoted strings, and executing `gopass` commands or other shell commands to fetch dynamic values.

After the initial parsing and command substitutions, `load-env` performs an iterative variable expansion process, resolving all `$VAR` and `${VAR}` references within the loaded environment variables, handling nesting and detecting circularities. Finally, based on your chosen mode and the `--sandboxed` flag:

- For running executables or launching subshells, `load-env` uses `syscall.Exec` to replace its own process with the target command, ensuring the environment is seamlessly passed. The environment passed includes variables from `.env` files, optionally merged with the inherited system environment based on the `--sandboxed` flag.
- For exporting variables, it prints shell-compatible `export` commands to standard output.
- For viewing, it simply prints the resolved variables to your terminal.

## Why `load-env`?

While other tools exist for managing `.env` files, `load-env` aims to be a minimal, self-contained Go binary that's easy to distribute and use, especially in environments where installing Python or Node.js dependencies might be cumbersome. It provides built-in `gopass` integration for those who manage secrets with it, and clear modes of operation for various workflows. Its ability to chain multiple `.env` files and offer sandboxed execution provides powerful flexibility for complex configurations.

## Installation

You can build `load-env` from source:

```bash
git clone [https://github.com/revivalstack/load-env.git](https://github.com/revivalstack/load-env.git)
cd load-env
go build -o load-env .
sudo mv load-env /usr/local/bin/
```

or via

```bash
go install [github.com/revivalstack/load-env@latest](https://github.com/revivalstack/load-env@latest)
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
BUILD_ID=$[date +%Y%m%d%H%M%S]                      # Example of robust command substitution using $[]
API_KEY="supersecret_key_with_\"quotes\""
APP_URL=http://$DB_HOST:$DB_PORT/app
MESSAGE='This is a literal string with $ and \' characters.'
```

### Chaining .env Files

To load `base.env` then `dev.env`, with `dev.env` overriding variables from `base.env`:

```bash
load-env base,dev myapp-script.sh
```

### Running an Executable

To load variables for `myproject` and then run a command:

```bash
load-env myproject node index.js --port 3000
```

### Launching a Subshell

To load variables for `myproject` and launch a new interactive shell:

```bash
load-env myproject
```

### Exporting to Current Shell

To load variables for `myproject` into your current shell session (variables will persist):

```bash
eval "$(load-env myproject --export)"
```

**Warning**: Variables exported this way are visible in your shell's environment (`env`, `ps e`) and can persist across commands. Use with caution for sensitive data.

### Viewing Variables

To see the resolved environment variables for `myproject` without running any commands:

```bash
load-env myproject --view
```

**Warning**: This will print plaintext secrets (if any are resolved from `gopass`) to your terminal.

### Sandboxed Execution

To run a command with _only_ the environment variables defined in `myproject.env`, ignoring inherited system environment variables (unless overridden):

```bash
load-env myproject --sandboxed bash -c 'echo "PATH: $PATH, MY_VAR: $MY_VAR"'
# MY_VAR will be whatever is in myproject.env, PATH will likely be empty if not explicitly set there.
```

### Version and Help

```bash
load-env --version
load-env --help
```

---

### Integration with Container Runtimes (Podman & Docker)

Seamlessly inject `load-env`'s variables into your containers:

1.  **Via `--env-file` (Podman or Docker):**
    Use `load-env --view` with your shell's process substitution (`<()`) to stream resolved variables as an environment file to your container runtime. This is the most robust method for both Podman and Docker.

    ```bash
    # For Podman:
    podman run --rm --env-file <(load-env base,dev --view) alpine env

    # For Docker:
    docker run --rm --env-file <(load-env base,dev --view) alpine env
    ```

    - _Note:_ Requires shells supporting process substitution (e.g., Bash, Zsh).
    - _Security:_ Output (including secrets) is streamed, not printed to terminal, but the command remains visible in shell history and process lists.

2.  **Via `--env-host` (Podman only):**
    Have `load-env` directly execute the `podman run` command. Use `--env-host` flag if you want environment variables set by `load-env` (and other host variables) to be passed into the container.

    ```bash
    # load-env sets env for the 'podman' process, and '--env-host' passes them to 'alpine'
    load-env base,dev podman run --rm --env-host alpine printenv
    ```

---

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

`load-env` is released under the [MIT License](https://github.com/revivalstack/load-env/blob/main/LICENSE).
