"""Efeitos de uma tool sobre o ESTADO do grafo — gancho genérico do `tools_node`.

Algumas tools, além de devolver um resultado ao modelo, precisam alterar o
estado persistido da conversa: o dry-run de chamado carimba a proposta que a
abertura vai executar, a abertura bem-sucedida a consome, a identificação
grava o dado informado pelo cliente. Em vez de o `tools_node` conhecer cada
tool por nome, ele chama `efeito_pos_tool(nome, resultado, state)` depois de
cada execução e aplica o delta devolvido.

`EFEITOS` é o registro: nome da tool → função `(nome, resultado, state) ->
delta`. Tool sem entrada não mexe no estado (no-op). O registro nasce VAZIO —
E2.3 (tools do Infodesk) preenche: carimbar `proposta_chamado` no
`preparar_abertura_chamado`, zerar no `abrir_chamado`, gravar `identificacao`
no `informar_identificacao`.

Invariantes para quem registrar um efeito:
- recebe o estado como leitura e devolve um dict NOVO (nunca muta `state`);
- nunca devolve `messages` (os `ToolMessage` são do `tools_node`) nem a
  identidade da sessão (`CHAVES_BLOQUEADAS`) — são descartadas com aviso;
- decide pelo `resultado` da tool (ex.: `status: true`), nunca por texto do
  modelo — o efeito é parte da base confiável, o LLM não.
"""

from __future__ import annotations

import logging
from collections.abc import Callable, Mapping
from typing import Any

logger = logging.getLogger(__name__)

Efeito = Callable[[str, Any, Mapping[str, Any]], Mapping[str, Any] | None]

#: Registro de efeitos por nome de tool — preenchido em E2.3.
EFEITOS: dict[str, Efeito] = {}

#: Chaves que um efeito NUNCA pode escrever: `messages` é do `tools_node`; a
#: identidade da sessão (telefone, conversa, contato) vem do gateway via
#: Contrato A, nunca do resultado de uma tool.
CHAVES_BLOQUEADAS: frozenset[str] = frozenset(
    {"messages", "telefone", "conversation_id", "contato", "nome_contato"}
)


def efeito_pos_tool(nome: str, resultado: Any, state: Mapping[str, Any]) -> dict[str, Any]:
    """Delta de estado produzido pela execução de `nome` (vazio por default).

    Fail-soft: um efeito que explode não derruba o turno — o resultado da tool
    já foi obtido; o erro vai para o log e o estado fica como estava.
    """
    efeito = EFEITOS.get(nome)
    if efeito is None:
        return {}
    try:
        delta = dict(efeito(nome, resultado, state) or {})
    except Exception as exc:
        logger.warning("efeito_pos_tool_falhou nome=%s erro=%s", nome, exc)
        return {}
    bloqueadas = CHAVES_BLOQUEADAS & set(delta)
    if bloqueadas:
        logger.warning("efeito_pos_tool_ignorou_chaves nome=%s chaves=%s", nome, sorted(bloqueadas))
        for chave in bloqueadas:
            delta.pop(chave)
    return delta
