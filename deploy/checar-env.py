#!/usr/bin/env python3
"""Confere o `.env.example` e o compose contra o código (E4.2).

Sem dependências além da stdlib. Rode da raiz do repositório:

    python deploy/checar-env.py

Verificações (qualquer falha = exit 1):

1. TODA variável de ambiente lida pelo gateway Go (`backend/internal/config`,
   `os.Getenv`/`os.LookupEnv` em `backend/**`) e pelo cérebro Python
   (`alias="…"` dos `BaseSettings`, `os.getenv`/`os.environ` em
   `agente-ia/src`, `server.py`, `app.py`) aparece no `.env.example`, ativa
   (`NOME=`) ou comentada (`# NOME=`).
2. O inverso: toda variável do `.env.example` é lida por algum código ou é
   variável só do compose (`${NOME}` / chave de `environment:` nos
   `docker-compose*.yml`). Pega nome que sobrou de um domínio antigo.
3. Nenhum valor de segredo (`*KEY`, `*TOKEN`, `*SECRET`, `*PASSWORD`, `*SAL`)
   preenchido no `.env.example`.
4. `docker compose config` valida base e base+prod (com valores falsos só para
   esta checagem). Pulado, com aviso, se o `docker` não estiver no PATH.
"""

from __future__ import annotations

import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

RAIZ = Path(__file__).resolve().parents[1]
NOME = r"[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*"
SEGREDO = re.compile(r"(KEY|TOKEN|SECRET|PASSWORD|SAL)$")
IGNORAR_DIRS = {"venv", ".venv", "node_modules", "__pycache__", ".git", "chroma_db", ".hf"}

# Valores com cara de credencial real, mesmo em linha comentada.
PADROES_SEGREDO = {
    "chave do OpenRouter (sk-or-…)": re.compile(r"sk-or-[A-Za-z0-9-]{8,}"),
    "chave do Infodesk (ik_live_…)": re.compile(r"ik_live_[A-Za-z0-9]{8,}"),
    "segredo de webhook (whsec_…)": re.compile(r"whsec_[A-Za-z0-9]{8,}"),
    "senha embutida em URL (usuário:senha@host)": re.compile(r"://[^/\s:@<]+:[^@\s<]+@"),
}

# Lidas por bibliotecas/ferramentas, não por código nosso, mas legítimas no .env.
EXTRAS_PERMITIDAS: set[str] = set()


def arquivos(base: Path, sufixo: str):
    for raiz, dirs, nomes in os.walk(base):
        dirs[:] = [d for d in dirs if d not in IGNORAR_DIRS]
        for n in nomes:
            if n.endswith(sufixo):
                yield Path(raiz) / n


def ler(p: Path) -> str:
    return p.read_text(encoding="utf-8", errors="replace")


def variaveis_go() -> set[str]:
    achadas: set[str] = set()
    for p in arquivos(RAIZ / "backend", ".go"):
        if p.name.endswith("_test.go"):
            continue
        achadas |= set(re.findall(rf'os\.(?:Getenv|LookupEnv)\("({NOME})"\)', ler(p)))
    # internal/config lê por helpers (getenv, lerBool, tabelas com o nome da
    # variável em literal): todo literal MAIÚSCULO do arquivo é nome de variável.
    cfg = RAIZ / "backend" / "internal" / "config" / "config.go"
    achadas |= set(re.findall(rf'"({NOME})"', ler(cfg)))
    return achadas


def variaveis_python() -> set[str]:
    achadas: set[str] = set()
    alvos = list(arquivos(RAIZ / "agente-ia" / "src", ".py"))
    alvos += [RAIZ / "agente-ia" / "server.py", RAIZ / "agente-ia" / "app.py"]
    for p in alvos:
        if not p.is_file():
            continue
        t = ler(p)
        achadas |= set(re.findall(rf'alias="({NOME})"', t))
        achadas |= set(re.findall(rf'os\.(?:getenv|environ\.get)\(\s*"({NOME})"', t))
        achadas |= set(re.findall(rf'os\.environ\[\s*"({NOME})"\s*\]', t))
    return achadas


def variaveis_compose() -> set[str]:
    achadas: set[str] = set()
    for p in sorted(RAIZ.glob("docker-compose*.yml")):
        t = ler(p)
        achadas |= set(re.findall(rf"\$\{{({NOME})", t))
        # chaves de `environment:` (mapa) — 6 espaços de indentação no nosso estilo
        achadas |= set(re.findall(rf"^ {{6}}({NOME}):", t, flags=re.M))
    return achadas


def variaveis_env_example() -> dict[str, str]:
    """nome → valor (string vazia se vazio ou comentado)."""
    out: dict[str, str] = {}
    for linha in ler(RAIZ / ".env.example").splitlines():
        m = re.match(rf"^#?\s*({NOME})=(.*)$", linha)
        if not m:
            continue
        nome, valor = m.group(1), m.group(2).strip()
        if not linha.lstrip().startswith("#"):
            out[nome] = valor
        else:
            out.setdefault(nome, "")
    return out


def checar_compose() -> list[str]:
    docker = shutil.which("docker")
    if not docker:
        print("AVISO: docker não está no PATH — `docker compose config` pulado.")
        return []
    env = {
        **os.environ,
        "PGVECTOR_PASSWORD": "senha-falsa-so-para-validar",
        "TOOLS_API_TOKEN": "token-falso-so-para-validar",
    }
    erros: list[str] = []
    combinacoes = {
        "base": ["docker-compose.yml"],
        "base+prod": ["docker-compose.yml", "docker-compose.prod.yml"],
    }
    for rotulo, arqs in combinacoes.items():
        cmd = [docker, "compose"]
        for a in arqs:
            cmd += ["-f", a]
        cmd += ["config", "--quiet"]
        # COMPOSE_FILE do ambiente não pode competir com os -f acima.
        e = {k: v for k, v in env.items() if k != "COMPOSE_FILE"}
        r = subprocess.run(cmd, cwd=RAIZ, env=e, capture_output=True, text=True)
        if r.returncode != 0:
            erros.append(f"docker compose config ({rotulo}) falhou: {r.stderr.strip()[:400]}")
        else:
            print(f"✔ docker compose config ({rotulo})")
    return erros


def main() -> int:
    # Console do Windows (cp1252) não imprime "✔"/acentos sem isto.
    for fluxo in (sys.stdout, sys.stderr):
        fluxo.reconfigure(encoding="utf-8")
    erros: list[str] = []
    go, py = variaveis_go(), variaveis_python()
    lidas = go | py
    compose = variaveis_compose()
    exemplo = variaveis_env_example()

    faltam = sorted(lidas - set(exemplo))
    if faltam:
        erros.append(".env.example não cita variáveis lidas pelo código: " + ", ".join(faltam))

    sobram = sorted(set(exemplo) - lidas - compose - EXTRAS_PERMITIDAS)
    if sobram:
        erros.append(
            ".env.example cita variáveis que nenhum código nem o compose usa: " + ", ".join(sobram)
        )

    preenchidas = sorted(
        n for n, v in exemplo.items() if SEGREDO.search(n) and v and not v.startswith("#")
    )
    if preenchidas:
        erros.append(
            "segredos com valor no .env.example (devem ficar vazios): " + ", ".join(preenchidas)
        )

    texto = ler(RAIZ / ".env.example")
    for rotulo, padrao in PADROES_SEGREDO.items():
        if padrao.search(texto):
            erros.append(f".env.example parece conter {rotulo} (mesmo em linha comentada)")

    if not erros:
        print(
            f"✔ .env.example cobre as {len(lidas)} variáveis do código "
            f"(Go {len(go)}, Python {len(py)}) e não tem segredo preenchido."
        )
    erros += checar_compose()

    for e in erros:
        print("ERRO:", e, file=sys.stderr)
    return 1 if erros else 0


if __name__ == "__main__":
    sys.exit(main())
