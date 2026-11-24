#!/usr/bin/env bash

main() (
  set -euo pipefail

  for tool in git go; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      printf 'agent-qa: %s is required\n' "$tool" >&2
      exit 1
    fi
  done

  work_dir=$(mktemp -d)
  trap 'find "$work_dir" -depth -delete' EXIT
  git clone --depth 1 --branch main https://github.com/aleclarson/agent-qa.git "$work_dir/source"
  cd "$work_dir/source"
  go build -o "$work_dir/agent-qa" .

  bin_dir="$HOME/.local/bin"
  mkdir -p "$bin_dir"
  if [ -d "$bin_dir/agent-qa" ]; then
    printf 'agent-qa: expected a file at %s/agent-qa\n' "$bin_dir" >&2
    exit 1
  fi
  install -m 0755 "$work_dir/agent-qa" "$bin_dir/agent-qa"

  add_to_path() {
    local file="$1"
    local line='case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) export PATH="$HOME/.local/bin:$PATH" ;; esac'
    if ! grep -Fqx -- "$line" "$file" 2>/dev/null; then
      printf '\n%s\n' "$line" >> "$file"
    fi
  }

  login_shell=${SHELL:-bash}
  case "${login_shell##*/}" in
    zsh)
      add_to_path "${ZDOTDIR:-$HOME}/.zshrc"
      ;;
    bash)
      add_to_path "$HOME/.bashrc"
      for profile in "$HOME/.bash_profile" "$HOME/.bash_login" "$HOME/.profile"; do
        if [ -r "$profile" ]; then
          break
        fi
      done
      add_to_path "$profile"
      ;;
  esac

  printf 'Installed agent-qa at %s/agent-qa\n' "$bin_dir"
  printf 'To use it in this terminal (Bash or Zsh), run:\n  export PATH="$HOME/.local/bin:$PATH"\n'
)

main "$@"
