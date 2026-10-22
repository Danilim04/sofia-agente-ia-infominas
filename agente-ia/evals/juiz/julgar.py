# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Chama o LLM juiz e lê a resposta (uma retentativa se o formato vier errado)."""

from __future__ import annotations

from typing import Any

from evals.juiz.cenarios import CenarioJuiz
from evals.juiz.execucao import Transcricao
from evals.juiz.parser import ErroJuiz, Julgamento, parse_resposta_juiz
from evals.juiz.prompt import ler_prompt, mensagem_do_juiz

LEMBRETE_FORMATO = (
    "Sua resposta anterior não seguiu o formato. Responda SOMENTE com o objeto JSON "
    '{"criterios": {"<criterio>": {"nota": <0-5>, "justificativa": "..."}}} para os critérios pedidos.'
)


def _texto(conteudo: Any) -> str:
    if isinstance(conteudo, str):
        return conteudo
    if isinstance(conteudo, list):  # blocos de conteúdo (str ou {"type": "text", "text": ...})
        return "".join(
            b if isinstance(b, str) else str(b.get("text", "")) for b in conteudo if b is not None
        )
    return str(conteudo)


def julgar(juiz_llm: Any, cenario: CenarioJuiz, transcricao: Transcricao) -> Julgamento:
    """Pede a nota ao juiz. `ErroJuiz` se a resposta não vier no formato (mesmo após reenviar)."""
    from langchain_core.messages import HumanMessage, SystemMessage

    mensagens: list[Any] = [
        SystemMessage(content=ler_prompt()),
        HumanMessage(content=mensagem_do_juiz(cenario, transcricao)),
    ]
    ultimo_erro: ErroJuiz | None = None
    for tentativa in range(2):
        resposta = juiz_llm.invoke(mensagens)
        texto = _texto(getattr(resposta, "content", resposta))
        try:
            return parse_resposta_juiz(texto, cenario.rubrica.criterios)
        except ErroJuiz as e:
            ultimo_erro = e
            if tentativa == 0:
                mensagens = [*mensagens, HumanMessage(content=LEMBRETE_FORMATO)]
    assert ultimo_erro is not None
    raise ultimo_erro
