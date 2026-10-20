"""Testes de arquitetura (camada 1 da esteira — docs/plano/04-esteira-qualidade.md).

Barram o que linter comum não vê:

1. **Camadas** do cérebro via import-linter (contratos em `pyproject.toml`).
2. **Resquícios do domínio antigo** (nomes do produto, do transporte, do banco
   e do SAC anteriores) fora dos caminhos ainda tolerados em
   `docs/plano/legado-permitido.txt` — a lista só encolhe; cada onda remove
   linhas.
3. **F7 novo**: toda tool exposta ao modelo tem entrada em `POLITICAS` e nenhum
   argumento de sessão aparece no schema que o LLM vê.
"""

from __future__ import annotations

import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

import pytest

AGENTE_IA = Path(__file__).resolve().parents[1]
RAIZ = AGENTE_IA.parent

# ---------------------------------------------------------------------------
# 1. Camadas (import-linter)
# ---------------------------------------------------------------------------


def test_camadas_import_linter():
    pytest.importorskip("importlinter")
    exe = shutil.which("lint-imports", path=str(Path(sys.executable).parent))
    cmd = [exe] if exe else [sys.executable, "-m", "importlinter.cli"]
    proc = subprocess.run(cmd, cwd=AGENTE_IA, capture_output=True, text=True, check=False)  # noqa: S603
    assert proc.returncode == 0, f"import-linter falhou:\n{proc.stdout}\n{proc.stderr}"


# ---------------------------------------------------------------------------
# 2. Termos do domínio antigo
# ---------------------------------------------------------------------------

# Termos partidos de propósito (`+` explícito — o formatador não junta): o
# próprio scanner é varrido e não pode conter, em claro, os termos que caça.
_TERMOS = (
    "azap" + "fy",
    "za" + "pin",
    "chat" + "woot",
    "mon" + "go",
    r"\bsa" + "c_",
    "azap" + "ers",
    "zp" + "rs",
    "zp" + "f-",
)
TERMOS_LEGADOS = re.compile("|".join(_TERMOS), re.IGNORECASE)
DIRETORIOS_VARRIDOS = (
    "agente-ia/src",
    "agente-ia/server.py",
    "agente-ia/app.py",
    "agente-ia/tests",
    "backend",
    "contratos",
    "mock-infodesk",
)
EXTENSOES = {".py", ".go", ".json", ".yaml", ".yml", ".toml", ".sh", ".mod", ".sum", ".txt", ".md"}
IGNORAR_PARTES = {
    "venv",
    ".venv",
    "__pycache__",
    ".git",
    "node_modules",
    "chroma_db",
    "data",
    "bruto",
}


def _legado_permitido() -> list[str]:
    arquivo = RAIZ / "docs" / "plano" / "legado-permitido.txt"
    if not arquivo.exists():
        return []
    linhas = [ln.strip() for ln in arquivo.read_text(encoding="utf-8").splitlines()]
    return [
        ln.replace("\\", "/").rstrip("/") + ("/" if ln.endswith("/") else "")
        for ln in linhas
        if ln and not ln.startswith("#")
    ]


def _permitido(rel: str, permitidos: list[str]) -> bool:
    return any(rel == p.rstrip("/") or rel.startswith(p) for p in permitidos)


def _arquivos_varridos():
    for base in DIRETORIOS_VARRIDOS:
        caminho = RAIZ / base
        if caminho.is_file():
            yield caminho
            continue
        if not caminho.is_dir():
            continue
        for raiz, dirs, nomes in os.walk(caminho):
            dirs[:] = [d for d in dirs if d not in IGNORAR_PARTES]
            for nome in nomes:
                p = Path(raiz) / nome
                if p.suffix in EXTENSOES and p.name != "legado-permitido.txt":
                    yield p


def test_sem_termos_do_dominio_antigo():
    permitidos = _legado_permitido()
    ocorrencias: list[str] = []
    for arquivo in _arquivos_varridos():
        rel = arquivo.relative_to(RAIZ).as_posix()
        if _permitido(rel, permitidos):
            continue
        try:
            texto = arquivo.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
        for n, linha in enumerate(texto.splitlines(), 1):
            if TERMOS_LEGADOS.search(linha):
                ocorrencias.append(f"{rel}:{n}: {linha.strip()[:100]}")
    assert not ocorrencias, (
        "termos do domínio antigo fora de docs/plano/legado-permitido.txt "
        "(limpe o arquivo; NÃO adicione linha à lista):\n" + "\n".join(ocorrencias[:40])
    )


def test_legado_permitido_so_tem_caminhos_existentes():
    """Linha que aponta para caminho inexistente já pode (deve) sair da lista."""
    permitidos = _legado_permitido()
    inexistentes = [p for p in permitidos if not (RAIZ / p.rstrip("/")).exists()]
    assert not inexistentes, f"remova de legado-permitido.txt (não existem mais): {inexistentes}"


# ---------------------------------------------------------------------------
# 3. Tools × política (F7)
# ---------------------------------------------------------------------------

ARGS_DE_SESSAO = {
    "telefone",
    "conversation_id",
    "classificacao",
    "proposta",
    "identificacao",
}


def _tools_e_politicas():
    from src.agent.graph import get_default_tools
    from src.agent.tool_policy import POLITICAS

    return get_default_tools(), POLITICAS


def test_toda_tool_exposta_tem_politica():
    tools, politicas = _tools_e_politicas()
    sem_politica = [t.name for t in tools if t.name not in politicas]
    assert not sem_politica, f"tools sem entrada em POLITICAS (tool_policy.py): {sem_politica}"


def test_nenhum_arg_de_sessao_no_schema_do_llm():
    tools, _ = _tools_e_politicas()
    vazamentos = []
    for t in tools:
        props = set(t.tool_call_schema.model_json_schema().get("properties", {}))
        expostos = props & ARGS_DE_SESSAO
        if expostos:
            vazamentos.append(f"{t.name}: {sorted(expostos)}")
    assert not vazamentos, (
        f"argumentos de sessão visíveis ao modelo (use InjectedToolArg): {vazamentos}"
    )


def test_args_injetados_pela_politica_sao_injected_tool_arg():
    """Tudo que a política injeta precisa estar FORA do schema do modelo."""
    tools, politicas = _tools_e_politicas()
    por_nome = {t.name: t for t in tools}
    problemas = []
    for nome, pol in politicas.items():
        tool = por_nome.get(nome)
        if tool is None:
            continue  # política órfã (rede de segurança para tool futura) é permitida
        props = set(tool.tool_call_schema.model_json_schema().get("properties", {}))
        expostos = set(pol.injetar) & props
        if expostos:
            problemas.append(f"{nome}: {sorted(expostos)}")
    assert not problemas, f"args injetados que o modelo ainda enxerga: {problemas}"
