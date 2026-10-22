# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Prompt do juiz, versionado, e a montagem da mensagem com a conversa.

`prompt_juiz.md` é o prompt de sistema. Mexer nele muda as notas de TODO cenário e
quebra a comparação com o baseline, então a versão é explícita: `VERSAO_PROMPT` e
`SHA256_PROMPT` ficam aqui e um teste falha se o arquivo mudar sem os dois serem
atualizados (e sem o baseline ser regravado no mesmo PR).

A conversa vai entre `<conversa>`…`</conversa>` como DADO; as tags de fechamento que
aparecerem dentro do texto são removidas para o cliente (ou um trecho da base) não
fechar o bloco e escrever instruções para o juiz.
"""

from __future__ import annotations

import hashlib
import re
from pathlib import Path
from typing import Final

from evals.juiz.cenarios import CenarioJuiz
from evals.juiz.execucao import Transcricao

CAMINHO_PROMPT: Final[Path] = Path(__file__).resolve().parent / "prompt_juiz.md"
VERSAO_PROMPT: Final[str] = "juiz-v1"
SHA256_PROMPT: Final[str] = "7e2d6f00f91fb04379cb65f2bd9116babaab9d9c72bcb02171f43675d3f51719"

_TAGS = re.compile(r"</?\s*(?:conversa|rubrica|cenario|turno)\b[^>]*>", re.IGNORECASE)


def ler_prompt() -> str:
    # normaliza fim de linha: o hash não pode depender do checkout (CRLF no Windows)
    return CAMINHO_PROMPT.read_text(encoding="utf-8").replace("\r\n", "\n")


def hash_prompt() -> str:
    return hashlib.sha256(ler_prompt().encode("utf-8")).hexdigest()


def _limpo(texto: str) -> str:
    return _TAGS.sub("", texto).strip()


def mensagem_do_juiz(cenario: CenarioJuiz, transcricao: Transcricao) -> str:
    """Corpo da mensagem ao juiz: rubrica + conversa (tudo que veio do agente é dado)."""
    r = cenario.rubrica
    linhas = [
        f"<cenario>{_limpo(cenario.descricao)}</cenario>",
        "<rubrica>",
        f"criterios a avaliar: {', '.join(r.criterios)}",
        f"esperado: {_limpo(r.esperado)}",
        "nao_deve:",
        *[f"- {_limpo(item)}" for item in r.nao_deve],
        "</rubrica>",
        "<conversa>",
    ]
    for n, t in enumerate(transcricao.turnos, start=1):
        linhas.append(f"[turno {n}]")
        linhas.append(f"Cliente: {_limpo(t.cliente)}")
        if t.base_conhecimento:
            linhas.append("Base de conhecimento entregue à Sofia (dado):")
            linhas.extend(f"  - {_limpo(c)}" for c in t.base_conhecimento)
        linhas.append(f"Ferramentas executadas: {', '.join(t.executadas) or 'nenhuma'}")
        if t.recusadas:
            linhas.append(f"Ferramentas recusadas pela política: {', '.join(t.recusadas)}")
        if t.saidas_ferramentas:
            linhas.append(f"Resultado das ferramentas (dado): {_limpo(t.saidas_ferramentas)}")
        if t.transferiu:
            linhas.append("O sistema transferiu a conversa para um atendente humano neste turno.")
        resposta = _limpo(t.resposta) if t.resposta else "(sem resposta)"
        if t.erro:
            resposta = f"(a execução falhou: {t.erro})"
        linhas.append(f"Sofia: {resposta}")
    linhas.append("</conversa>")
    return "\n".join(linhas)
