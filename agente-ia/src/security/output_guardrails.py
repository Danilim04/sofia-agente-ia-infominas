"""Output guardrails — defesa contra prompt injection indireta (LLM01 indireta)
e integridade da resposta final ao cliente.

Entrada de dado externo (RAG/tools) → `<documento_externo>`:

1. **Delimitar:** envolver o conteúdo em `<documento_externo source="...">...</documento_externo>`,
   pareando com a regra explícita do `SYSTEM_PROMPT_AGENTE` de tratar tudo
   ali dentro como DADO (não COMANDO).
2. **Sanitizar:** XML-escapar `<`, `>` e `&` no conteúdo, impedindo que o
   atacante feche o delimitador com `</documento_externo>` ou injete tags
   de chat (`<system>`, `<user>`, etc.) que confundam o modelo.

Saída final ao cliente (usadas pelo `output_guardrail_node`):

3. **Citações verificadas (F4):** `validar_citacoes` remove da resposta
   qualquer "(fonte: ...)" que não corresponda a um documento REALMENTE
   recuperado no turno — citação fabricada torna-se impossível por construção
   (o modelo pode escrever, mas o texto não sobrevive até o cliente).
4. **Máscara de erro interno (A2/LLM06):** `contem_vazamento_interno` detecta
   marcas inequívocas de erro técnico (traceback, módulos, hosts internos) —
   o nó troca a resposta inteira por um fallback amigável.

Não tentamos remover instruções imperativas em linguagem natural ("ignore
o sistema") — isso é responsabilidade da regra anti-injection do system
prompt + classificador de input.
"""

from __future__ import annotations

import html
import logging
import re
import unicodedata
from typing import Iterable, Mapping


logger = logging.getLogger(__name__)


def _sanitizar_conteudo(texto: object) -> str:
    """XML-escape de `<`, `>`, `&` no conteúdo + remoção de NULs.

    `quote=False` mantém `"` e `'` legíveis (só importam em atributos).
    """
    if not isinstance(texto, str):
        texto = "" if texto is None else str(texto)
    texto = texto.replace("\x00", "")
    return html.escape(texto, quote=False)


def _escape_attr(valor: object) -> str:
    if not isinstance(valor, str):
        valor = "" if valor is None else str(valor)
    return html.escape(valor, quote=True)


def envolver_dado_externo(
    conteudo: object,
    source: str = "desconhecido",
    **extra_attrs: object,
) -> str:
    """Envolve conteúdo externo em `<documento_externo ...>...</documento_externo>`.

    Args:
        conteudo: texto bruto vindo de RAG, web ou outra tool externa.
        source: identificador de origem (nome do arquivo, URL, etc.).
        **extra_attrs: atributos opcionais (`secao="..."`, `origem="rag"`, ...).

    Returns:
        String pronta para concatenar a uma mensagem que o LLM vai ler.
    """
    pares = [f'source="{_escape_attr(source)}"']
    for chave, valor in extra_attrs.items():
        pares.append(f'{chave}="{_escape_attr(valor)}"')
    abertura = "<documento_externo " + " ".join(pares) + ">"
    return f"{abertura}\n{_sanitizar_conteudo(conteudo)}\n</documento_externo>"


# ---------------------------------------------------------------------------
# Citações verificadas (F4) — a citação que chega ao cliente é necessariamente
# de um documento recuperado de verdade neste turno.
# ---------------------------------------------------------------------------

# Formato instruído no system prompt: (fonte: <source>, seção "<secao>").
_CITACAO_RE = re.compile(r"\(\s*fontes?\s*:[^)]*\)", re.IGNORECASE)


def _normalizar_para_busca(texto: str) -> str:
    """Minúsculas sem acento — comparação tolerante entre citação e metadado."""
    nfkd = unicodedata.normalize("NFKD", texto or "")
    return "".join(c for c in nfkd if not unicodedata.combining(c)).lower()


def _termos_permitidos(fontes: Iterable[str]) -> list[str]:
    """Explode os rótulos de fonte ("arquivo.md — Seção") em termos verificáveis."""
    termos: list[str] = []
    for rotulo in fontes or []:
        for parte in str(rotulo).split("—"):
            parte = _normalizar_para_busca(parte).strip()
            # descarta pedaços curtos demais para servirem de evidência
            if len(parte) >= 4:
                termos.append(parte)
    return termos


def validar_citacoes(texto: str, fontes: Iterable[str]) -> tuple[str, list[str]]:
    """Remove citações "(fonte: ...)" que não batem com as fontes recuperadas.

    Args:
        texto: resposta final do agente.
        fontes: rótulos dos documentos recuperados no turno (`fontes_usadas`).

    Returns:
        `(texto_limpo, citacoes_removidas)`.
    """
    if not texto:
        return texto, []
    permitidos = _termos_permitidos(fontes)
    removidas: list[str] = []

    def _checar(m: re.Match[str]) -> str:
        citacao = m.group(0)
        alvo = _normalizar_para_busca(citacao)
        if any(termo in alvo for termo in permitidos):
            return citacao
        removidas.append(citacao)
        return ""

    limpo = _CITACAO_RE.sub(_checar, texto)
    if removidas:
        # arruma espaços/pontuação órfãos deixados pela remoção
        limpo = re.sub(r"[ \t]+([.,;:!?\n])", r"\1", limpo)
        limpo = re.sub(r"[ \t]{2,}", " ", limpo).strip()
        logger.warning("citacoes_fabricadas_removidas total=%d", len(removidas))
    return limpo, removidas


# ---------------------------------------------------------------------------
# Máscara de erro interno (A2) — marcas que NUNCA pertencem a uma resposta de
# atendimento. Substring simples, case-insensitive, deliberadamente estreitas
# (o cliente pode falar de "erro 500 no relatório" — isso não dispara).
# ---------------------------------------------------------------------------

_MARCAS_INTERNAS = (
    "traceback",
    'file "/',
    "httpx.",
    "sqlite3.",
    "chromadb",
    "pydantic",
    "localhost",
    "127.0.0.1",
    "x-tools-token",
    "openrouter",
    "stack trace",
)


def contem_vazamento_interno(texto: str) -> bool:
    """True quando a resposta contém marca inequívoca de erro/infra interna."""
    alvo = (texto or "").lower()
    return any(marca in alvo for marca in _MARCAS_INTERNAS)


def envolver_chunks_rag(chunks: Iterable[Mapping[str, object]]) -> str:
    """Formata os chunks devolvidos por `consultar_base_conhecimento`.

    Espera dicts no formato `{texto, secao, source}` (saída da tool RAG).
    Chunks vazios são ignorados.
    """
    blocos: list[str] = []
    for chunk in chunks:
        texto = chunk.get("texto")
        if not texto:
            continue
        source = str(chunk.get("source") or "desconhecido")
        extras: dict[str, object] = {"origem": "rag"}
        secao = chunk.get("secao")
        if secao:
            extras["secao"] = secao
        blocos.append(envolver_dado_externo(texto, source=source, **extras))
    return "\n\n".join(blocos)