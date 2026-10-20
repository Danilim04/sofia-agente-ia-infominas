# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Runner dos cenários de conversa — ESQUELETO (execução entra em E3.5).

Hoje: descobre os YAMLs em `evals/conversa/`, valida com `evals.formatos` e,
quando um cenário é executado, marca `xfail` com a pendência. Em E3.5 o
`executar()` monta `build_graph(llm=fake_llm(script), tools=get_default_tools(),
buscar_chunks=sem_rag, jev=FakeJEV(cenario.jev))` com `httpx` apontado para o
`gateway_falso(contratos/)` e checa cada `espera` do turno. Os fakes vêm de
`tests/fakes.py` (compartilhados com os testes; nunca copiar aqui).

Dependências de produto (E2.2 classificação, E2.3 tools, E1.6 contratos)
ainda não existem neste ramo, então este módulo NÃO importa `src`.
"""

from __future__ import annotations

import importlib.util
from collections.abc import Sequence
from pathlib import Path
from typing import Final

from evals.formatos import CenarioConversa, Taxonomia, carregar_cenario, carregar_taxonomia

DIRETORIO: Final[Path] = Path(__file__).resolve().parent
PENDENCIA: Final[str] = (
    "runner entra em E3.5 (precisa de E2.2 classificação, E2.3 tools e E1.6 contratos)"
)


def arquivos_de_cenario(diretorio: Path = DIRETORIO) -> list[Path]:
    """Todos os `*.yaml` do diretório (inclui o TEMPLATE, que deve ser válido), ordenados."""
    return sorted(p for p in diretorio.glob("*.yaml") if p.is_file())


def carregar_cenarios(
    diretorio: Path = DIRETORIO, taxonomia: Taxonomia | None = None
) -> list[CenarioConversa]:
    """Carrega e valida todos os cenários; ids têm de ser únicos."""
    if taxonomia is None:
        taxonomia = carregar_taxonomia()
    cenarios = [carregar_cenario(p, taxonomia.categorias) for p in arquivos_de_cenario(diretorio)]
    vistos: dict[str, Path] = {}
    for c in cenarios:
        assert c.arquivo is not None
        if c.id in vistos:
            raise ValueError(f"{c.arquivo.name}: id '{c.id}' repetido (já em {vistos[c.id].name})")
        vistos[c.id] = c.arquivo
    return cenarios


def grafo_disponivel() -> bool:
    """`src.agent.graph.build_graph` existe? (sem importar: evita puxar LLM/RAG)."""
    return importlib.util.find_spec("src.agent.graph") is not None


def executar(cenario: CenarioConversa) -> None:
    """Executa um cenário contra o grafo. TODO(E3.5): implementar; hoje é xfail."""
    import pytest

    pytest.xfail(f"{cenario.id}: {PENDENCIA}")


def resumo(cenarios: Sequence[CenarioConversa]) -> str:
    linhas = [f"{len(cenarios)} cenário(s) válido(s) em {DIRETORIO}:"]
    for c in cenarios:
        nome = c.arquivo.name if c.arquivo else "?"
        linhas.append(f"  {c.id:<14} {len(c.turnos)} turno(s)  {nome}  — {c.descricao}")
    linhas.append(
        f"execução: pendente ({PENDENCIA}); build_graph {'encontrado' if grafo_disponivel() else 'ausente'}"
    )
    return "\n".join(linhas)
