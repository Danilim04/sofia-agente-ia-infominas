#!/usr/bin/env bash
# Aponta o git deste clone para .githooks/ (pre-commit = scripts/verificar.sh --rapido).
set -e
cd "$(dirname "${BASH_SOURCE[0]}")/.."
git config core.hooksPath .githooks
chmod +x .githooks/* scripts/*.sh .claude/hooks/*.sh 2>/dev/null || true
echo "hooks do git instalados (core.hooksPath=.githooks)"
