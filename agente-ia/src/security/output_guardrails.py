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
5. **Afirmação de ação sem ferramenta (ZE-2):** `afirmacoes_de_acao` acha
   "registrei / deixei o recado / avisei o time / abri o chamado" em 1ª
   pessoa; o nó troca a resposta pelo fallback quando NENHUMA ferramenta
   executou com sucesso no turno (fora de uma tool, o "recado" não existe).

Não tentamos remover instruções imperativas em linguagem natural ("ignore
o sistema") — isso é responsabilidade da regra anti-injection do system
prompt + classificador de input.
"""

from __future__ import annotations

import html
import logging
import re
import unicodedata
from collections.abc import Iterable, Mapping

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


def remover_citacoes(texto: str) -> tuple[str, list[str]]:
    """Remove TODA citação "(fonte: ...)" da resposta final.

    Decisão de produto (2026-09-10): o cliente não recebe referência de
    documentação — nem nome de arquivo, nem seção. A auditabilidade (LLM09)
    continua no estado (`fontes_usadas`), não no texto enviado.

    Returns:
        `(texto_limpo, citacoes_removidas)`.
    """
    if not texto:
        return texto, []
    removidas = [m.group(0) for m in _CITACAO_RE.finditer(texto)]
    if not removidas:
        return texto, []
    limpo = _CITACAO_RE.sub("", texto)
    limpo = re.sub(r"[ \t]+([.,;:!?\n])", r"\1", limpo)
    limpo = re.sub(r"[ \t]{2,}", " ", limpo).strip()
    logger.info("citacoes_removidas_da_resposta total=%d", len(removidas))
    return limpo, removidas


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


# ---------------------------------------------------------------------------
# Afirmação de ação sem ferramenta (ZE-2) — "registrei / deixei o recado /
# avisei o time / abri o chamado" só é verdade quando uma ferramenta executou
# com sucesso no turno; quem sabe isso é o `output_guardrail_node` (estado).
# Aqui fica só a detecção textual: determinística, sem acento e sem caixa.
# Deliberadamente estreita — 1ª pessoa (eu/nós) e ação CONCLUÍDA ou em curso.
# Promessa futura ("vou registrar"), infinitivo/how-to ("você pode registrar"),
# 3ª pessoa ("você registrou"), fala citada entre aspas e negação ("ainda não
# registrei") NÃO disparam.
# ---------------------------------------------------------------------------

_ALVO_TIME = (
    r"(?:(?:para|pro|pra|ao|a|com)\s+)?(?:o\s+|a\s+|nosso\s+|nossa\s+)?"
    r"(?:time|equipe|setor|suporte|pessoal|tecnic\w*|desenvolv\w*|devs?|"
    r"responsav\w*|area)\b"
)
_OBJETO_CASO = (
    r"(?:isso|isto|tudo|o\s+caso|a\s+situacao|o\s+problema|o\s+relato|"
    r"a\s+ocorrencia|a\s+solicitacao|a\s+demanda|"
    r"(?:sua|seu)\s+(?:solicitacao|caso|pedido|relato|demanda|problema|situacao))"
)
_AFIRMACOES_DE_ACAO_RE = tuple(
    re.compile(p)
    for p in (
        # eu/nós + passado: registrei, anotei, acionei, protocolei...
        (
            r"\b(?:ja\s+)?(?:registrei|anotei|acionei|notifiquei|sinalizei|protocolei|"
            r"cadastrei|registramos|anotamos|acionamos|notificamos|sinalizamos|"
            r"protocolamos|cadastramos)\b"
        ),
        # "deixei (o recado) registrado/anotado" · "deixei um recado/aviso"
        r"\bdeixei\b(?:\s+\w+){0,4}?\s+(?:registrad|anotad|encaminhad|sinalizad|avisad)\w*",
        (
            r"\bdeixei\s+(?:o\s+|um\s+|uma\s+|seu\s+|sua\s+)?"
            r"(?:recado|aviso|alerta|registro|anotacao|observacao)\b"
        ),
        # encaminhei/repassei + o caso / sua solicitação
        (r"\b(?:ja\s+)?(?:encaminhei|repassei|encaminhamos|repassamos)\s+" + _OBJETO_CASO + r"\b"),
        # comunicação ao time: avisei/falei/passei/pedi/encaminhei ... (para) o time
        (
            r"\b(?:ja\s+)?(?:avisei|comuniquei|alertei|informei|falei|passei|pedi|solicitei|"
            r"encaminhei|repassei|avisamos|comunicamos|alertamos|informamos|falamos|"
            r"passamos|pedimos|solicitamos|encaminhamos|repassamos)\s+"
            r"(?:" + _OBJETO_CASO + r"\s+)?" + _ALVO_TIME
        ),
        # estado concluído: "já está/foi registrado" · "sua solicitação foi encaminhada"
        (
            r"\bja\s+(?:esta|estao|foi|foram|ficou|ficaram)\s+(?:devidamente\s+|tudo\s+)?"
            r"(?:registrad|anotad|encaminhad|repassad|sinalizad|protocolad)\w*"
        ),
        (
            r"\b(?:solicitacao|pedido|recado|demanda|relato|caso|chamado|ticket|protocolo)\s+"
            r"(?:ja\s+)?(?:esta|foi|ficou)\s+(?:devidamente\s+)?"
            r"(?:registrad|anotad|encaminhad|repassad|sinalizad|protocolad|abert|criad|gerad)\w*"
        ),
        r"\brecado\s+(?:registrad|anotad|encaminhad|repassad)\w*",
        # chamado/ticket/protocolo: "abri o chamado" · "chamado criado com sucesso"
        (
            r"\b(?:ja\s+)?(?:abri|criei|gerei|abrimos|criamos|geramos)\s+(?:aqui\s+)?"
            r"(?:o\s+|um\s+|seu\s+|esse\s+|este\s+)?(?:chamado|ticket|protocolo)\b"
        ),
        r"\b(?:chamado|ticket)\s+(?:aberto|registrado|criado)\s+com\s+sucesso\b",
        # em curso: "estou acionando/registrando ..."
        (
            r"\b(?:estou|estamos)\s+(?:registrando|anotando|encaminhando|repassando|"
            r"acionando|sinalizando|notificando|protocolando)\b"
        ),
    )
)
# Negação logo antes da afirmação ("ainda não registrei", "não abri o chamado").
_NEGACAO_ANTES_RE = re.compile(r"\b(?:nao|nunca|nem|jamais|sem)\s+(?:\w+\s+)?$")
# "se" (condicional/incerteza) na MESMA oração, antes da afirmação: "não consegui
# confirmar se o chamado foi registrado", "vou ver se anotaram". Não é afirmar a
# ação — é dizer que não se sabe. A oração termina em pontuação ou "mas".
_FRONTEIRA_ORACAO_RE = re.compile(r"[.,;:!?\n]|\bmas\b")
_CONDICIONAL_RE = re.compile(r"\bse\b")
# Trechos entre aspas são fala citada (em geral do cliente) — não contam.
_TRECHO_CITADO_RE = re.compile(r"[\"“”«»]([^\"“”«»]{1,240})[\"“”«»]")


def afirmacoes_de_acao(texto: str | None) -> list[str]:
    """Trechos da resposta que AFIRMAM ação concluída/em curso em 1ª pessoa.

    Registro, anotação, aviso/encaminhamento ao time ou abertura de chamado —
    coisas que só acontecem por ferramenta. Devolve os trechos casados
    (normalizados: minúsculas, sem acento) para log/teste; lista vazia = nada
    afirmado. Fala citada entre aspas, afirmações negadas e construções de
    incerteza/condição ("não consegui confirmar SE o chamado foi registrado")
    são ignoradas — estas últimas só valem com o "se" na mesma oração (sem vírgula
    ou ponto entre ele e a afirmação), então "abri o chamado, se precisar me chame"
    segue barrado.
    """
    if not texto:
        return []
    alvo = _normalizar_para_busca(_TRECHO_CITADO_RE.sub(" ", str(texto)))
    achadas: list[str] = []
    for padrao in _AFIRMACOES_DE_ACAO_RE:
        for m in padrao.finditer(alvo):
            antes = alvo[max(0, m.start() - 24) : m.start()]
            if _NEGACAO_ANTES_RE.search(antes):
                continue
            oracao = _FRONTEIRA_ORACAO_RE.split(alvo[max(0, m.start() - 60) : m.start()])[-1]
            if _CONDICIONAL_RE.search(oracao):
                continue
            achadas.append(m.group(0))
    return achadas


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
