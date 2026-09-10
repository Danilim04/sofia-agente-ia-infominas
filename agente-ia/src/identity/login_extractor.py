"""Extrator de login via LLM — fallback do gate de identidade (backend Go).

O gate Go resolve o login deterministicamente (mensagem crua + normalização de
CPF/CNPJ). Quando isso falha — login não-numérico embutido numa frase, ex.
"meu login é joao" — ele chama `POST /extract-login`, que roda este extrator.

Segurança: o valor devolvido é apenas um CANDIDATO. A autorização real continua
no Go (lookup no Mongo + confirmação de um dado). O LLM não autentica ninguém;
no pior caso devolve um login inexistente ou que falha na confirmação. O system
prompt trata a mensagem como DADO (nunca COMANDO) — defesa contra LLM01.

Política de falha: fail-soft. Erro de rede/parse → `login=None` (o Go segue como
"não encontrado" e pede de novo).
"""

from __future__ import annotations

import logging
from typing import Callable, Optional

from pydantic import BaseModel, Field


logger = logging.getLogger(__name__)


class LoginExtraido(BaseModel):
    """Schema estruturado da resposta do extrator."""

    login: Optional[str] = Field(
        default=None,
        description=(
            "O identificador que o usuário forneceu (CPF, CNPJ, e-mail ou "
            "username), exatamente como escrito; null se não houver nenhum."
        ),
    )


def _extrair_via_llm(mensagem: str) -> dict:
    """Roda o extrator no LLM barato (mesmo modelo do classificador)."""
    from langchain_core.messages import HumanMessage, SystemMessage

    from src.agent.llm import get_classifier_llm
    from src.agent.prompts import SYSTEM_PROMPT_EXTRATOR_LOGIN

    try:
        llm = get_classifier_llm().with_structured_output(LoginExtraido)
        resultado: LoginExtraido = llm.invoke(  # type: ignore[assignment]
            [
                SystemMessage(content=SYSTEM_PROMPT_EXTRATOR_LOGIN),
                HumanMessage(content=mensagem),
            ]
        )
    except Exception as exc:  # noqa: BLE001 — fail-soft com log
        logger.warning("extrator_login_indisponivel erro=%s", exc)
        return {"login": None, "erro": str(exc)}

    login = (resultado.login or "").strip()
    return {"login": login or None}


def extrair_login(
    mensagem: str,
    *,
    extrator: Optional[Callable[[str], dict]] = None,
) -> dict:
    """Extrai o login de uma mensagem em linguagem natural.

    Args:
        mensagem: texto cru do usuário (ex.: "meu login é joao").
        extrator: injeção de dependência para testes — função `(mensagem)->dict`
            com chave `login`. Default: `_extrair_via_llm`.

    Returns:
        `{"login": str | None}`.
    """
    if not isinstance(mensagem, str) or not mensagem.strip():
        return {"login": None}

    extrator = extrator or _extrair_via_llm
    resultado = extrator(mensagem)
    login = resultado.get("login")
    logger.info("login_extraido login=%r", login)
    return {"login": login}
