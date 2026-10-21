"""Carimbo da proposta de chamado — compartilhado por `efeitos` e `tool_policy`.

Vive em `src.tools` (e não em `tool_policy`) porque o efeito do
`preparar_abertura_chamado` precisa carimbar a proposta e a camada `tools` não
pode importar a política (`pyproject.toml`, contratos do import-linter). A
política importa daqui e re-exporta `carimbar_proposta`.
"""

from __future__ import annotations

from collections.abc import Iterable, Mapping
from datetime import UTC, datetime
from typing import Any

from langchain_core.messages import BaseMessage, HumanMessage


def relogio() -> datetime:
    """Relógio do carimbo (UTC) — separado para os testes congelarem o tempo."""
    return datetime.now(UTC)


def turno_atual(state: Mapping[str, Any]) -> int:
    """Número do turno = quantidade de mensagens do cliente no histórico.

    Derivado do histórico persistido (nunca de um contador que o modelo possa
    influenciar): no turno N o `tools_node` vê N `HumanMessage`.
    """
    mensagens: Iterable[BaseMessage] = state.get("messages") or []
    return sum(1 for m in mensagens if isinstance(m, HumanMessage))


def carimbar_proposta(
    proposta: Mapping[str, Any],
    state: Mapping[str, Any],
    agora: datetime | None = None,
) -> dict[str, Any]:
    """Cópia da proposta aprovada pelo dry-run com `criada_em` (ISO 8601, UTC)
    e `turno` (turno em que foi preparada). É esse carimbo que `abrir` confere.
    """
    quando = agora or relogio()
    return {
        **dict(proposta),
        "criada_em": quando.astimezone(UTC).isoformat(),
        "turno": turno_atual(state),
    }
