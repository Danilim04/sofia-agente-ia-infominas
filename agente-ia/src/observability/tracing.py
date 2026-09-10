"""Tracing opcional via Langfuse (observabilidade do cérebro).

Liga o grafo LangGraph ao Langfuse por meio do `CallbackHandler` da integração
LangChain. Cada requisição do Contrato A vira um trace, agrupado por conversa
(`langfuse_session_id` = `conversation_id`), com o custo/latência/tool_calls de
cada turno — é o que permite analisar um teste de ponta a ponta sem garimpar
log + banco.

**Fail-safe (regra de ouro):** o tracing NUNCA pode derrubar o atendimento.
- Sem `LANGFUSE_PUBLIC_KEY`/`LANGFUSE_SECRET_KEY`, fica DESLIGADO e o agente roda
  normal (é o estado de dev/testes e do período até as chaves serem preenchidas).
- Qualquer erro de configuração (import faltando, credencial inválida) é logado e
  vira "sem tracing", nunca uma exceção que estoure no `/chat`.

Config em `src/config.py` (lidos do `.env`): `LANGFUSE_PUBLIC_KEY`,
`LANGFUSE_SECRET_KEY`, `LANGFUSE_HOST` (default EU: https://cloud.langfuse.com;
US: https://us.cloud.langfuse.com).
"""

from __future__ import annotations

import logging
from functools import lru_cache
from typing import Any, Optional


logger = logging.getLogger(__name__)


@lru_cache(maxsize=1)
def get_tracing_handler() -> Optional[Any]:
    """Devolve um `CallbackHandler` do Langfuse, ou `None` se o tracing estiver off.

    O handler é seguro para reuso entre requisições (os dados por-turno vêm no
    `config` da invocação), então é criado uma única vez por processo.
    """
    from src.config import get_settings

    settings = get_settings()
    if not settings.langfuse_enabled:
        logger.info(
            "langfuse_tracing desativado (sem LANGFUSE_PUBLIC_KEY/SECRET_KEY)"
        )
        return None

    try:
        from langfuse import Langfuse
        from langfuse.langchain import CallbackHandler

        # Inicializa o cliente-singleton que o CallbackHandler consome. Passamos
        # as credenciais explicitamente para não depender do nome da env var.
        Langfuse(
            public_key=settings.langfuse_public_key,
            secret_key=settings.langfuse_secret_key,
            host=settings.langfuse_host,
        )
        handler = CallbackHandler()
        logger.info("langfuse_tracing ativo host=%s", settings.langfuse_host)
        return handler
    except Exception as exc:  # noqa: BLE001 — tracing jamais derruba o atendimento
        logger.warning(
            "langfuse_tracing_falhou erro=%s — seguindo SEM tracing", exc
        )
        return None
