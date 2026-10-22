"""Efeitos de uma tool sobre o ESTADO do grafo — gancho genérico do `tools_node`.

Algumas tools, além de devolver um resultado ao modelo, precisam alterar o
estado persistido da conversa: o dry-run de chamado carimba a proposta que a
abertura vai executar, a abertura bem-sucedida a consome, a identificação
grava o dado informado pelo cliente. Em vez de o `tools_node` conhecer cada
tool por nome, ele chama `efeito_pos_tool(nome, resultado, state)` depois de
cada execução e aplica o delta devolvido.

`EFEITOS` é o registro: nome da tool → função `(nome, resultado, state) ->
delta`. Tool sem entrada não mexe no estado (no-op). Efeitos das tools do
Infodesk (E2.3), ao final deste módulo:

- `preparar_abertura_chamado` ok → carimba `proposta_chamado` (`criada_em`,
  `turno`); qualquer outro resultado zera a proposta (o estado reflete sempre
  o último preparo);
- `abrir_chamado` ok OU falha → consome (`proposta_chamado = None`). Depois de
  uma tentativa que o gateway executou (mesmo sem resposta, `infodesk_indisponivel`)
  não há segundo `abrir` com a mesma proposta no mesmo turno: o modelo teria de
  preparar de novo, e o `preparar` do gateway devolve `chamado_aberto` se o ticket
  existir (dedup) — "nunca retry em POST /tickets" (regra 3). Recusa da política
  não chama o efeito: a proposta fica como estava;
- `informar_identificacao` ok → grava `identificacao = {forma,
  valor_mascarado}` — só o mascarado em `identificacao`. ATENÇÃO: o `AIMessage`
  que chamou a tool leva o CPF/contrato em `tool_calls[].args` e vai para o
  checkpoint e o trace como qualquer mensagem; mascarar isso é do checkpointer/nós
  (fora da zona da E2.3) — TODO(follow-up da E2.3), nota no PR.

- `cadastrar_solicitante` ok → grava `identificacao = {forma: "cadastro",
  valor_mascarado: <nome abreviado>, requester_id}` (E5.3, ADR 0009): o telefone passou
  a existir no Infodesk e `exigir_identificado` passa. Não há efeito de
  `preparar_cadastro_solicitante`: a proposta é lida do histórico pela política
  (`tool_policy._cadastro_preparado`), não de um campo do estado.

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

from langchain_core.messages import AIMessage

from src.identity.contrato import mascarar_contrato
from src.identity.cpf import cpf_valido, mascarar_cpf
from src.tools.proposta import carimbar_proposta

logger = logging.getLogger(__name__)

Efeito = Callable[[str, Any, Mapping[str, Any]], Mapping[str, Any] | None]

#: Registro de efeitos por nome de tool (preenchido ao final do módulo).
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


# ---------------------------------------------------------------------------
# Efeitos das tools do Infodesk (E2.3)
# ---------------------------------------------------------------------------


def _ok(resultado: Any) -> bool:
    """Sucesso = o gateway disse `status: true` (nunca texto do modelo)."""
    return isinstance(resultado, Mapping) and resultado.get("status") is True


def _efeito_preparar(nome: str, resultado: Any, state: Mapping[str, Any]) -> dict[str, Any]:
    proposta = resultado.get("proposta") if _ok(resultado) else None
    if isinstance(proposta, Mapping) and proposta:
        return {"proposta_chamado": carimbar_proposta(proposta, state)}
    return {"proposta_chamado": None}


def _efeito_abrir(nome: str, resultado: Any, state: Mapping[str, Any]) -> dict[str, Any]:
    return {"proposta_chamado": None}


def _args_da_chamada(nome: str, state: Mapping[str, Any]) -> list[Mapping[str, Any]]:
    """Args propostos pelo modelo nas chamadas a `nome` do último `AIMessage`.

    O resultado da tool não devolve o dado informado; a máscara é calculada a
    partir dele aqui, em memória, e o dado em claro não vai a lugar nenhum.
    """
    for mensagem in reversed(list(state.get("messages") or [])):
        if isinstance(mensagem, AIMessage):
            return [
                tc.get("args") or {} for tc in (mensagem.tool_calls or []) if tc.get("name") == nome
            ]
    return []


def _efeito_identificacao(nome: str, resultado: Any, state: Mapping[str, Any]) -> dict[str, Any]:
    if not _ok(resultado) or resultado.get("identificado") is not True:
        return {}
    forma = resultado.get("forma")
    if forma not in ("cpf", "contrato"):
        return {}
    mascarado = mascarar_cpf("") if forma == "cpf" else mascarar_contrato("")
    for args in _args_da_chamada(nome, state):
        if forma == "cpf" and cpf_valido(args.get("cpf")):
            mascarado = mascarar_cpf(args.get("cpf"))
        elif forma == "contrato" and args.get("contrato"):
            mascarado = mascarar_contrato(args.get("contrato"))
    return {"identificacao": {"forma": forma, "valor_mascarado": mascarado}}


def _efeito_cadastro(nome: str, resultado: Any, state: Mapping[str, Any]) -> dict[str, Any]:
    """Cadastro concluído → o cliente passa a estar identificado (forma `cadastro`)."""
    if not _ok(resultado) or resultado.get("cadastrado") is not True:
        return {}
    solicitante = resultado.get("solicitante")
    abreviado = solicitante.get("nome") if isinstance(solicitante, Mapping) else ""
    identificacao: dict[str, Any] = {"forma": "cadastro", "valor_mascarado": str(abreviado or "")}
    if resultado.get("requester_id"):
        identificacao["requester_id"] = str(resultado["requester_id"])
    return {"identificacao": identificacao}


EFEITOS.update(
    {
        "preparar_abertura_chamado": _efeito_preparar,
        "abrir_chamado": _efeito_abrir,
        "informar_identificacao": _efeito_identificacao,
        "cadastrar_solicitante": _efeito_cadastro,
    }
)
