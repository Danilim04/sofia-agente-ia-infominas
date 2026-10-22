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
# Incerteza honesta não é afirmação de ação: "não consegui confirmar SE o chamado
# foi registrado", "não sei se registrei direito". Só vale com CABEÇA epistêmica
# ("confirmar", "saber", "ver", "ter certeza"...) logo antes do "se" (até 3 palavras
# entre os dois) na MESMA oração — um "se" solto não isenta ("Se quiser eu abri o
# chamado", "o sinal se perdeu, por isso registrei"). A oração termina em
# pontuação, travessão, parêntese ou conector ("mas", "e", "então", "porém"...).
_FRONTEIRA_ORACAO_RE = re.compile(
    r"[.,;:!?\n()\-\u2013\u2014]|\b(?:mas|e|entao|porem|pois|dai|ai|so que)\b"
)
_CABECA_EPISTEMICA = (
    r"(?:confirm\w*|sei|sabe\w*|sabia\w*|saber|ver|verific\w*|conferi\w*|checar|"
    r"certeza|dizer|garant\w*|descobr\w*|duvid\w*|afirmar)"
)
_ATE_A_AFIRMACAO = r"(?:\s+\w+){0,3}\s*$"
#: "…confirmar se" + até 3 palavras antes da afirmação ("se o chamado ja foi...").
_INCERTEZA_SE_RE = re.compile(
    r"\b" + _CABECA_EPISTEMICA + r"\b(?:\s+\w+){0,3}\s+se\b" + _ATE_A_AFIRMACAO
)
#: "não tenho certeza de que…", "não consigo confirmar que…", "não posso garantir
#: que…" — o "que" só isenta quando há negação antes da cabeça.
_INCERTEZA_QUE_RE = re.compile(
    r"\b(?:nao|sem)\b(?:\s+\w+){0,3}?\s+"
    + _CABECA_EPISTEMICA
    + r"\b(?:\s+de)?(?:\s+\w+){0,2}\s+que\b"
    r"(?:\s+\w+){0,2}\s*$"
)
#: Oração condicional que abre direto no sujeito da afirmação: "se o chamado
#: já foi aberto…" ("se" + artigo/possessivo e nada mais antes do trecho casado).
_CONDICIONAL_DIRETA_RE = re.compile(r"\bse\s+(?:o|a|os|as|seu|sua|esse|essa|este|esta)?\s*$")
# Trechos entre aspas são fala citada (em geral do cliente) — não contam.
_TRECHO_CITADO_RE = re.compile(r"[\"“”«»]([^\"“”«»]{1,240})[\"“”«»]")


def afirmacoes_de_acao(texto: str | None) -> list[str]:
    """Trechos da resposta que AFIRMAM ação concluída/em curso em 1ª pessoa.

    Registro, anotação, aviso/encaminhamento ao time ou abertura de chamado —
    coisas que só acontecem por ferramenta. Devolve os trechos casados
    (normalizados: minúsculas, sem acento) para log/teste; lista vazia = nada
    afirmado. Fala citada entre aspas, afirmações negadas e incerteza honesta
    ("não consegui confirmar SE o chamado foi registrado", "não tenho certeza de
    QUE foi registrado") são ignoradas. A incerteza exige cabeça epistêmica
    ("confirmar", "saber", "ver", "ter certeza"...) antes do "se"/"que" na mesma
    oração; um "se" solto não isenta ("Se quiser eu abri o chamado" segue barrado).
    """
    if not texto:
        return []
    return _varrer(_alvo(texto), _AFIRMACOES_DE_ACAO_RE)


def _alvo(texto: str | None) -> str:
    """Texto normalizado (minúsculas, sem acento) sem a fala citada entre aspas."""
    return _normalizar_para_busca(_TRECHO_CITADO_RE.sub(" ", str(texto or "")))


def _varrer(alvo: str, padroes: Iterable[re.Pattern[str]]) -> list[str]:
    """Trechos de `alvo` que casam `padroes`, menos negação e incerteza honesta."""
    achadas: list[str] = []
    for padrao in padroes:
        for m in padrao.finditer(alvo):
            antes = alvo[max(0, m.start() - 24) : m.start()]
            if _NEGACAO_ANTES_RE.search(antes):
                continue
            oracao = _FRONTEIRA_ORACAO_RE.split(alvo[: m.start()])[-1]
            if (
                _INCERTEZA_SE_RE.search(oracao)
                or _INCERTEZA_QUE_RE.search(oracao)
                or _CONDICIONAL_DIRETA_RE.search(oracao)
            ):
                continue
            achadas.append(m.group(0))
    return achadas


# ---------------------------------------------------------------------------
# Afirmações TIPADAS (E4.5b, B2): "chamado aberto", "te transferi", "cadastro
# concluído" só são verdade quando a ferramenta DAQUELE efeito executou com
# sucesso no turno — outra tool qualquer (um `preparar`, um `listar`) não ancora.
# Quem cruza o tipo com o que executou é o `output_guardrail_node`.
# ---------------------------------------------------------------------------

TIPO_ABERTURA = "abertura"  # 1ª pessoa: "abri/criei o chamado"
TIPO_CHAMADO_ABERTO = "chamado_aberto"  # passivo: "chamado 4821 foi aberto/criado"
TIPO_TRANSFERENCIA = "transferencia"
TIPO_CADASTRO = "cadastro"

_SUBST_CHAMADO = r"(?:chamado|ticket|protocolo)"
#: número opcional logo após o substantivo: "chamado 4821", "chamado nº 4821", "#4821"
_NUM_OBRIGATORIO = r"\s*(?:n[o°º.]*\s*|numero\s*|\#\s*)?\d{2,}"
_PARTICIPIO_ABERTURA = r"(?:abert[oa]s?|criad[oa]s?|gerad[oa]s?|registrad[oa]s?|cadastrad[oa]s?)"
#: sem "aberto": "o chamado 4821 aberto" costuma descrever estado ("você já tem o chamado
#: 4821 aberto"); só com "foi/ficou" ("foi aberto") ou com os verbos de criação vale.
_PARTICIPIO_CRIACAO = r"(?:criad[oa]s?|gerad[oa]s?|registrad[oa]s?|cadastrad[oa]s?)"

#: "o número do seu chamado é 48213" · "seu protocolo é 99887" · "protocolo: 99887"
_ANUNCIO_DE_NUMERO = (
    r"\b(?:(?:o\s+)?numero\s+d[oa]\s+(?:seu\s+|sua\s+)?(?:chamado|ticket|protocolo)|"
    r"(?:seu|o)\s+(?:protocolo|ticket|chamado))(?:\s+e\b|\s*:)\s*(?:n[o°º.]*\s*)?(\d{2,})\b"
)

_AFIRMACOES_TIPADAS_RE: dict[str, tuple[re.Pattern[str], ...]] = {
    TIPO_ABERTURA: (
        re.compile(
            r"\b(?:ja\s+)?(?:abri|criei|gerei|registrei|abrimos|criamos|geramos|registramos)\s+"
            r"(?:aqui\s+)?(?:o\s+|um\s+|seu\s+|o\s+seu\s+|esse\s+|este\s+)?"
            + _SUBST_CHAMADO
            + r"\b"
        ),
        # "já abri pra você" · "abri uma solicitação para a equipe"
        re.compile(r"\b(?:ja\s+)?(?:abri|criei|gerei)\s+(?:pra|para)\s+(?:voce|vc)\b"),
        re.compile(
            r"\b(?:ja\s+)?(?:abri|criei|gerei)\s+(?:uma|a)\s+(?:solicitacao|ocorrencia|demanda)\b"
        ),
    ),
    TIPO_CHAMADO_ABERTO: (
        # "chamado 4821 foi aberto" · "chamado 4821 criado" · "protocolo 4821 gerado"
        # (sem "está aberto": é status; e "chamado aberto" solto, sem número nem "foi",
        # é só um substantivo qualificado: "você tem algum chamado aberto?")
        re.compile(
            r"\b" + _SUBST_CHAMADO + _NUM_OBRIGATORIO + r"\s+(?:ja\s+)?(?:foi\s+|ficou\s+)?"
            r"(?:devidamente\s+)?" + _PARTICIPIO_CRIACAO + r"\b"
        ),
        re.compile(
            r"\b"
            + _SUBST_CHAMADO
            + f"(?:{_NUM_OBRIGATORIO})?"
            + r"\s+(?:ja\s+)?(?:foi|ficou)\s+(?:devidamente\s+)?"
            + _PARTICIPIO_ABERTURA
            + r"\b"
        ),
        # "chamado aberto com sucesso" · "seu chamado está aberto e a equipe já foi avisada"
        re.compile(r"\b(?:chamado|ticket)\s+(?:aberto|criado|registrado)\s+com\s+sucesso\b"),
        re.compile(r"\b(?:chamado|ticket)\s+esta\s+aberto\s+e\s+a\s+equipe\b"),
        # "o número do seu chamado é 48213" · "seu protocolo é 99887"
        re.compile(_ANUNCIO_DE_NUMERO),
        # "foi aberto o chamado 4821"
        re.compile(
            r"\bfoi\s+"
            + _PARTICIPIO_ABERTURA
            + r"\s+(?:o\s+|um\s+|seu\s+)?"
            + _SUBST_CHAMADO
            + r"\b"
        ),
    ),
    TIPO_TRANSFERENCIA: (
        re.compile(
            r"\b(?:ja\s+)?(?:transferi|transferimos|passei|passamos|encaminhei|encaminhamos)\s+"
            r"(?:voce|vc|a\s+conversa|sua\s+conversa|a\s+sua\s+conversa|o\s+atendimento|"
            r"seu\s+atendimento)\b"
        ),
        re.compile(r"\b(?:estou|estamos)\s+(?:te\s+|lhe\s+)?(?:transferindo|passando)\b"),
        re.compile(r"\bte\s+(?:transferi|passei|encaminhei)\b"),
        re.compile(r"\b(?:um\s+)?atendente\s+(?:ja\s+)?foi\s+(?:acionad|chamad|avisad)[oa]\b"),
        re.compile(r"\btransferencia\s+(?:ja\s+)?(?:concluida|realizada|feita)\b"),
        re.compile(
            r"\b(?:voce|conversa|atendimento)\s+(?:ja\s+)?foi\s+(?:transferid|encaminhad|passad)[oa]s?\b"
        ),
    ),
    TIPO_CADASTRO: (
        re.compile(r"\b(?:ja\s+)?(?:te\s+)?(?:cadastrei|cadastramos)\b"),
        re.compile(r"\b(?:voce\s+)?(?:ja\s+)?(?:esta|foi)\s+cadastrad[oa]\b"),
        re.compile(
            r"\bcadastro\s+(?:ja\s+)?(?:(?:foi|esta|ficou)\s+)?(?:devidamente\s+)?"
            r"(?:concluid|feito|realizad|finalizad|criad|efetuad|registrad)[oa]s?\b"
        ),
    ),
}

#: "chamado 4821", "ticket nº 4821", "protocolo #4821", "chamado número 4821"
_NUMERO_CITADO_RE = re.compile(
    r"\b" + _SUBST_CHAMADO + r"s?\s*(?:n[o°º.]*\s*|numero\s*|\#\s*)?(\d{2,})\b"
)
_NUMERO_ANUNCIADO_RE = re.compile(_ANUNCIO_DE_NUMERO)


def afirmacoes_tipadas(texto: str | None) -> dict[str, list[str]]:
    """Afirmações de ação por TIPO (abertura, chamado_aberto, transferencia, cadastro).

    Mesmas regras de `afirmacoes_de_acao` (fala citada, negação e incerteza
    honesta não contam); só devolve os tipos que apareceram.
    """
    if not texto:
        return {}
    alvo = _alvo(texto)
    achadas = {tipo: _varrer(alvo, padroes) for tipo, padroes in _AFIRMACOES_TIPADAS_RE.items()}
    return {tipo: trechos for tipo, trechos in achadas.items() if trechos}


def numeros_de_chamado_citados(texto: str | None) -> list[str]:
    """Números de chamado/ticket/protocolo citados na resposta (só dígitos, sem repetir)."""
    if not texto:
        return []
    vistos: list[str] = []
    alvo = _alvo(texto)
    for regex in (_NUMERO_CITADO_RE, _NUMERO_ANUNCIADO_RE):
        for m in regex.finditer(alvo):
            if m.group(1) not in vistos:
                vistos.append(m.group(1))
    return vistos


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
