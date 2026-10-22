"""Tracing opcional via Langfuse (observabilidade do cérebro).

Liga o grafo LangGraph ao Langfuse por meio do `CallbackHandler` da integração
LangChain. Cada requisição do Contrato A vira um trace, agrupado por conversa
(`session_id` = `conversation_id`), com o custo/latência/tool_calls de cada
turno e as TAGS `canal:`, `intencao:`, `categoria:` e `origem:` (filtráveis no
painel) — é o que permite analisar um teste de ponta a ponta sem garimpar log
+ banco.

**Sem PII no trace (regra 5 do CLAUDE.md):** o cliente Langfuse é criado com
`mask=mascarar_dado` (`observability/pii.py`), que o SDK aplica a `input`,
`output` e `metadata` de TODO span — inclusive o de entrada de tool
(`on_tool_start`), onde a política injeta o `telefone`. O `user_id` é o telefone
mascarado; as tags só levam rótulos da taxonomia (validados abaixo).

**Fail-safe (regra de ouro):** o tracing NUNCA pode derrubar o atendimento.
- Sem `LANGFUSE_PUBLIC_KEY`/`LANGFUSE_SECRET_KEY`, fica DESLIGADO e o agente roda
  normal (é o estado de dev/testes e do período até as chaves serem preenchidas).
- Qualquer erro de configuração ou de envio (import faltando, credencial
  inválida) é logado e vira "sem tracing", nunca uma exceção que estoure no `/chat`.

Config em `src/config.py` (lidos do `.env`): `LANGFUSE_PUBLIC_KEY`,
`LANGFUSE_SECRET_KEY`, `LANGFUSE_HOST` (default EU: https://cloud.langfuse.com;
US: https://us.cloud.langfuse.com).
"""

from __future__ import annotations

import logging
import re
from collections.abc import Mapping
from functools import lru_cache
from typing import Any

from src.observability.pii import mascarar_dado, mascarar_telefone

logger = logging.getLogger(__name__)

#: Rótulo seguro para virar tag: só o que a taxonomia/roteador produzem. Texto
#: livre (um LLM de fallback pode devolver qualquer coisa) NÃO vira tag.
_ROTULO = re.compile(r"[a-z0-9_.-]{1,60}")


@lru_cache(maxsize=1)
def get_langfuse() -> Any | None:
    """Cliente Langfuse com a máscara de PII, ou `None` se o tracing estiver off."""
    from src.config import get_settings

    settings = get_settings()
    if not settings.langfuse_enabled:
        logger.info("langfuse_tracing desativado (sem LANGFUSE_PUBLIC_KEY/SECRET_KEY)")
        return None

    try:
        from langfuse import Langfuse

        # Cliente-singleton que o CallbackHandler consome. Credenciais explícitas
        # para não depender do nome da env var; `mask` roda em todo span.
        cliente = Langfuse(
            public_key=settings.langfuse_public_key,
            secret_key=settings.langfuse_secret_key,
            host=settings.langfuse_host,
            mask=mascarar_dado,
        )
        logger.info("langfuse_tracing ativo host=%s", settings.langfuse_host)
        return cliente
    except Exception as exc:
        logger.warning("langfuse_tracing_falhou erro=%s — seguindo SEM tracing", type(exc).__name__)
        return None


@lru_cache(maxsize=1)
def get_tracing_handler() -> Any | None:
    """Devolve um `CallbackHandler` do Langfuse, ou `None` se o tracing estiver off.

    O handler é seguro para reuso entre requisições (os dados por-turno vêm no
    `config` da invocação), então é criado uma única vez por processo.
    """
    if get_langfuse() is None:
        return None
    try:
        from langfuse.langchain import CallbackHandler

        return CallbackHandler()
    except Exception as exc:
        logger.warning("langfuse_tracing_falhou erro=%s — seguindo SEM tracing", type(exc).__name__)
        return None


def _rotulo(prefixo: str, valor: object) -> str | None:
    texto = str(valor or "").strip().lower()
    return f"{prefixo}:{texto}" if _ROTULO.fullmatch(texto) else None


def tags_do_turno(canal: str, classificacao: Mapping[str, Any] | None = None) -> list[str]:
    """`["canal:whatsapp", "intencao:reclamacao", "categoria:sem_conexao", "origem:jev"]`.

    Sem classificação (turno que ainda não classificou ou falhou) só `canal:`.
    Valor fora do formato de rótulo é omitido, nunca mascarado em texto livre.
    """
    c = classificacao or {}
    tags = [
        _rotulo("canal", canal),
        _rotulo("intencao", c.get("intencao")),
        _rotulo("categoria", c.get("categoria")),
        _rotulo("origem", c.get("origem")),
    ]
    return [t for t in tags if t]


class TracoDoTurno:
    """Trace raiz de um turno; as tags de classificação entram DEPOIS do grafo rodar.

    Uso (`server.processar_chat`):

        with TracoDoTurno(conversation_id=..., canal=..., telefone=...) as traco:
            valores = await graph.ainvoke(...)        # o handler aninha neste trace
            traco.marcar_classificacao(valores.get("classificacao"))

    Com o tracing desligado (ou `cliente=None`) é um no-op. Erro do SDK nunca
    sobe: só vira um warning sem PII. Exceção do CORPO do `with` passa intacta.
    """

    def __init__(
        self,
        *,
        conversation_id: str,
        canal: str,
        telefone: str | None = None,
        cliente: Any | None = None,
    ) -> None:
        self._cliente = cliente if cliente is not None else get_langfuse()
        self._conversation_id = conversation_id
        self._canal = canal
        self._telefone = telefone
        self._cm: Any = None
        self._span: Any = None

    def __enter__(self) -> TracoDoTurno:
        if self._cliente is None:
            return self
        try:
            cm = self._cliente.start_as_current_span(name="chat")
            self._span = cm.__enter__()
            self._cm = cm  # só depois de entrar: __exit__ nunca fecha o que não abriu
            self._span.update_trace(
                session_id=self._conversation_id,
                user_id=mascarar_telefone(self._telefone) if self._telefone else None,
                tags=tags_do_turno(self._canal),
            )
        except Exception as exc:
            self._desistir("abrir", exc)
        return self

    def marcar_classificacao(self, classificacao: Mapping[str, Any] | None) -> None:
        """Acrescenta `intencao:`, `categoria:` e `origem:` às tags do trace."""
        if self._span is None or not classificacao:
            return
        try:
            self._span.update_trace(tags=tags_do_turno(self._canal, classificacao))
        except Exception as exc:
            self._desistir("marcar", exc)

    def __exit__(self, tipo: Any, valor: Any, tb: Any) -> None:
        cm, self._cm, self._span = self._cm, None, None
        if cm is None:
            return
        try:
            cm.__exit__(tipo, valor, tb)
        except Exception as exc:
            logger.warning("langfuse_tracing_falhou etapa=fechar erro=%s", type(exc).__name__)

    def _desistir(self, etapa: str, exc: Exception) -> None:
        logger.warning("langfuse_tracing_falhou etapa=%s erro=%s", etapa, type(exc).__name__)
        self._span = None
